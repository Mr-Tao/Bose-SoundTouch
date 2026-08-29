// Package webtypes contains type definitions for the SoundTouch web UI.
package webtypes

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
)

// SoundTouchClient defines the interface for SoundTouch client operations
type SoundTouchClient interface {
	Play() error
	Pause() error
	Stop() error
	NextTrack() error
	PrevTrack() error
	SetVolume(level int) error
	SetBass(level int) error
	SelectPreset(id int) error
	SelectSource(source, account string) error
	SendKey(key string) error
	GetDeviceInfo() (*models.DeviceInfo, error)
	GetNowPlaying() (*models.NowPlaying, error)
	GetVolume() (*models.Volume, error)
	GetPresets() (*models.Presets, error)
	GetSources() (*models.Sources, error)
	GetBass() (*models.Bass, error)
	NewWebSocketClient(config interface{}) *client.WebSocketClient
}

// DeviceConnection wraps a SoundTouch client with WebSocket connection.
//
// The Status field is stored behind atomic.Pointer so concurrent
// readers (HTTP handlers, WebSocket broadcasters) never observe a
// torn struct while a writer (UpdateDeviceStatus, WebSocket event
// handlers) is mid-update. Access status through Status / SetStatus
// / UpdateStatus rather than the private field; construct connections
// via NewDeviceConnection to guarantee the status is initialised.
type DeviceConnection struct {
	Client     *client.Client
	WebSocket  *client.WebSocketClient
	DeviceInfo *models.DeviceInfo
	LastSeen   time.Time

	status atomic.Pointer[DeviceStatus]

	statusOrderMu             sync.Mutex
	nextStatusPollGeneration  uint64
	lastStatusPollGeneration  uint64
	speakerEventGeneration    uint64
	statusPollEventGeneration map[uint64]uint64

	// groupMu orders polled /getGroup responses against real-time
	// groupUpdated events. Starting a newer refresh or receiving an event
	// invalidates any older in-flight poll.
	groupMu         sync.Mutex
	groupGeneration uint64

	// zoneMu protects the last topology confirmed by a zone master. Member
	// responses and failed refreshes must not dissolve a logical zone.
	zoneMu         sync.Mutex
	zoneGeneration uint64

	// done is closed by Close when the device is removed from the
	// registry, signalling its background goroutines (the status poller
	// and the WebSocket reconnect loop) to exit. closeOnce keeps Close
	// idempotent.
	done      chan struct{}
	closeOnce sync.Once
}

// DeviceStatus represents the current device state
type DeviceStatus struct {
	NowPlaying   *models.NowPlaying `json:"nowPlaying,omitempty"`
	Volume       *models.Volume     `json:"volume,omitempty"`
	Presets      *models.Presets    `json:"presets,omitempty"`
	Sources      *models.Sources    `json:"sources,omitempty"`
	Bass         *models.Bass       `json:"bass,omitempty"`
	Group        *models.Group      `json:"group,omitempty"`
	Zone         *models.ZoneInfo   `json:"zone,omitempty"`
	IsConnected  bool               `json:"isConnected"`
	LastActivity time.Time          `json:"lastActivity"`
}

// NewDeviceConnection creates a fully-initialised connection. The
// status starts with IsConnected=false and LastActivity set to now;
// real values arrive via UpdateStatus once the device responds.
func NewDeviceConnection(c *client.Client, info *models.DeviceInfo) *DeviceConnection {
	conn := &DeviceConnection{
		Client:     c,
		DeviceInfo: info,
		LastSeen:   time.Now(),
		done:       make(chan struct{}),
	}
	conn.status.Store(&DeviceStatus{
		IsConnected:  false,
		LastActivity: time.Now(),
	})

	return conn
}

// Status returns a snapshot of the current device status. The returned
// pointer is read-only from the caller's perspective and must not be
// mutated. Use UpdateStatus or SetStatus to apply changes. Never returns
// nil for connections built via NewDeviceConnection.
func (c *DeviceConnection) Status() *DeviceStatus {
	return c.status.Load()
}

// Done returns a channel that is closed when the connection is removed
// from the registry. The per-device status poller and WebSocket
// reconnect loop select on it to stop instead of running for the life
// of the process.
func (c *DeviceConnection) Done() <-chan struct{} {
	return c.done
}

// Close signals the connection's background goroutines to stop and best-
// effort disconnects the WebSocket so a blocked reconnect loop wakes
// promptly. Idempotent; safe to call on a connection that never started
// any goroutine (e.g. a test connection with a nil Client).
func (c *DeviceConnection) Close() {
	c.closeOnce.Do(func() {
		close(c.done)

		if c.WebSocket != nil {
			_ = c.WebSocket.Disconnect()
		}
	})
}

// SetStatus atomically replaces the entire status. Use sparingly —
// UpdateStatus is the preferred entry point because it preserves
// concurrent changes from other goroutines.
func (c *DeviceConnection) SetStatus(s *DeviceStatus) {
	c.status.Store(s)
}

// BeginStatusPoll reserves an ordering generation before a status poll starts
// network I/O. CompleteStatusPoll uses it to reject an older poll after either
// a newer poll or a real-time speaker event has supplied fresher state.
func (c *DeviceConnection) BeginStatusPoll() uint64 {
	c.statusOrderMu.Lock()
	defer c.statusOrderMu.Unlock()

	c.nextStatusPollGeneration++
	if c.statusPollEventGeneration == nil {
		c.statusPollEventGeneration = make(map[uint64]uint64)
	}

	c.statusPollEventGeneration[c.nextStatusPollGeneration] = c.speakerEventGeneration

	return c.nextStatusPollGeneration
}

// ApplySpeakerEvent serializes a real-time speaker event against status-poll
// completion. Even a duplicate event invalidates polls that started before it:
// the event is newer evidence than their fetched payload.
func (c *DeviceConnection) ApplySpeakerEvent(mut func(*DeviceStatus)) {
	c.statusOrderMu.Lock()
	defer c.statusOrderMu.Unlock()

	c.speakerEventGeneration++
	c.UpdateStatus(mut)
}

// CompleteStatusPoll applies a poll result only when no newer poll has already
// completed and no speaker event arrived after this poll began.
func (c *DeviceConnection) CompleteStatusPoll(
	generation uint64,
	mut func(*DeviceStatus),
) bool {
	c.statusOrderMu.Lock()
	defer c.statusOrderMu.Unlock()

	eventGeneration, knownGeneration := c.statusPollEventGeneration[generation]
	delete(c.statusPollEventGeneration, generation)

	if generation <= c.lastStatusPollGeneration {
		return false
	}

	c.lastStatusPollGeneration = generation
	for olderGeneration := range c.statusPollEventGeneration {
		if olderGeneration < generation {
			delete(c.statusPollEventGeneration, olderGeneration)
		}
	}

	if !knownGeneration || eventGeneration != c.speakerEventGeneration {
		return false
	}

	c.UpdateStatus(mut)

	return true
}

// UpdateStatus atomically applies mut to a copy of the current status
// and stores the result. If another goroutine updates the status while
// mut runs, UpdateStatus retries with the newer status — so concurrent
// writers cannot silently lose each other's changes.
//
// The copy mut receives is a shallow value copy of the previous status.
// Nested pointer fields (NowPlaying, Volume, Presets, Sources, Bass, Group,
// Zone)
// share their backing struct with the previous version: callers MUST
// REPLACE these pointers (s.Volume = &models.Volume{...}) rather than
// mutate through them (s.Volume.ActualVolume++ would race with any
// reader still holding the previous snapshot). Production callers
// receive these values fresh from the device API, so this is the
// natural shape.
func (c *DeviceConnection) UpdateStatus(mut func(*DeviceStatus)) {
	for {
		old := c.status.Load()
		next := *old
		mut(&next)

		if c.status.CompareAndSwap(old, &next) {
			return
		}
	}
}

// BeginGroupRefresh starts a new generation for an asynchronous /getGroup
// request. Only the latest started request may later update Group.
func (c *DeviceConnection) BeginGroupRefresh() uint64 {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	c.groupGeneration++

	return c.groupGeneration
}

// ApplyPolledGroup stores a /getGroup result only when no newer poll or
// groupUpdated event superseded it. Empty groups clear the current claim.
func (c *DeviceConnection) ApplyPolledGroup(generation uint64, group *models.Group) bool {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	if generation != c.groupGeneration {
		return false
	}

	return c.replaceGroup(normalizeGroup(group), time.Time{})
}

// ApplyGroupEvent stores the newest groupUpdated event and invalidates all
// in-flight /getGroup requests. Empty teardown events clear the current claim.
func (c *DeviceConnection) ApplyGroupEvent(group *models.Group, activity time.Time) bool {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	c.groupGeneration++

	return c.replaceGroup(normalizeGroup(group), activity)
}

func (c *DeviceConnection) replaceGroup(group *models.Group, activity time.Time) bool {
	changed := !reflect.DeepEqual(c.Status().Group, group)
	c.UpdateStatus(func(s *DeviceStatus) {
		s.Group = group
		if !activity.IsZero() {
			s.LastActivity = activity
		}
	})

	return changed
}

func normalizeGroup(group *models.Group) *models.Group {
	if group == nil || group.IsEmpty() {
		return nil
	}

	return group
}

// BeginZoneRefresh starts a new generation for an asynchronous master
// /getZone request. Starting a new refresh invalidates any older response.
func (c *DeviceConnection) BeginZoneRefresh() uint64 {
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()

	c.zoneGeneration++

	return c.zoneGeneration
}

// ApplyPolledZone stores topology only when it was returned by the queried
// master and no newer refresh superseded it. A standalone response from the
// queried master authoritatively clears the cache; member responses and
// malformed masterless responses are ignored.
func (c *DeviceConnection) ApplyPolledZone(
	generation uint64,
	queriedDeviceID string,
	zone *models.ZoneInfo,
) bool {
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()

	if generation != c.zoneGeneration || zone == nil {
		return false
	}

	master := strings.TrimSpace(zone.Master)
	queriedDeviceID = strings.TrimSpace(queriedDeviceID)
	if queriedDeviceID == "" ||
		(master == "" && len(zone.Members) != 0) ||
		(master != "" && master != queriedDeviceID) {
		return false
	}

	return c.replaceZone(normalizeZone(zone))
}

func (c *DeviceConnection) replaceZone(zone *models.ZoneInfo) bool {
	changed := !reflect.DeepEqual(c.Status().Zone, zone)
	if !changed {
		return false
	}

	c.UpdateStatus(func(status *DeviceStatus) {
		status.Zone = zone
	})

	return true
}

func normalizeZone(zone *models.ZoneInfo) *models.ZoneInfo {
	if zone == nil {
		return nil
	}

	deviceIDs := make(map[string]struct{}, len(zone.Members)+1)
	if master := strings.TrimSpace(zone.Master); master != "" {
		deviceIDs[master] = struct{}{}
	}

	for _, member := range zone.Members {
		if deviceID := strings.TrimSpace(member.DeviceID); deviceID != "" {
			deviceIDs[deviceID] = struct{}{}
		}
	}

	if len(deviceIDs) < 2 {
		return nil
	}

	return zone
}

// APIResponse is a standard JSON response wrapper
type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// VolumeRequest represents a volume control request
type VolumeRequest struct {
	Level int `json:"level"`
}

// BassRequest represents a bass control request
type BassRequest struct {
	Level int `json:"level"`
}

// WebSocketMessage represents messages sent over WebSocket
type WebSocketMessage struct {
	Type     string      `json:"type"`
	DeviceID string      `json:"deviceId,omitempty"`
	Data     interface{} `json:"data,omitempty"`
}

// DiscoveryStatus represents the status of device discovery
type DiscoveryStatus struct {
	IsDiscovering bool   `json:"isDiscovering"`
	Status        string `json:"status,omitempty"`
	DeviceCount   int    `json:"deviceCount,omitempty"`
}
