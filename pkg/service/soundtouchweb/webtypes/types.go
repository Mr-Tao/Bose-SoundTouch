// Package webtypes contains type definitions for the SoundTouch web UI.
package webtypes

import (
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

	// fieldGenMu guards fieldGen, the per-field generation ordering used by
	// BeginFieldPoll/CompleteFieldPoll/ApplyFieldEvent. Each StatusField gets
	// its own (issued, applied) pair so an event or a poll completion for
	// one field can never invalidate a different field's in-flight result --
	// see StatusField's doc comment.
	fieldGenMu sync.Mutex
	fieldGen   [numStatusFields]struct{ issued, applied uint64 }

	// groupMu orders polled /getGroup responses against real-time
	// groupUpdated events. groupGeneration is the highest generation
	// issued (by BeginGroupRefresh or ApplyGroupEvent);
	// groupAppliedGeneration is the highest generation whose result was
	// actually applied. Invalidation keys off completion order via
	// groupAppliedGeneration, not merely "a newer refresh has started" —
	// otherwise a later poll that starts but never applies (e.g. its own
	// GetGroup fails) would still discard an earlier poll's still-arriving
	// successful result.
	groupMu                sync.Mutex
	groupGeneration        uint64
	groupAppliedGeneration uint64

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
	IsConnected  bool               `json:"isConnected"`
	LastActivity time.Time          `json:"lastActivity"`
}

// StatusField identifies one independently-racing field of DeviceStatus for
// BeginFieldPoll/CompleteFieldPoll/ApplyFieldEvent's generation ordering.
// FieldConnectivity covers IsConnected, which is derived by a poll (from
// whether any of the other fields' fetches succeeded) but set directly by a
// real-time connectionStateUpdated event, so it races the same way the other
// fields do. Group has its own, pre-existing pair-aware ordering
// (groupMu/groupGeneration/groupAppliedGeneration below) and is not part of
// this set.
type StatusField int

// The StatusField values.
const (
	FieldNowPlaying StatusField = iota
	FieldVolume
	FieldPresets
	FieldSources
	FieldBass
	FieldConnectivity
	numStatusFields
)

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

// BeginFieldPoll reserves a generation for an asynchronous fetch of field,
// before any network I/O starts. Pass the returned value to CompleteFieldPoll
// once the fetch completes.
func (c *DeviceConnection) BeginFieldPoll(field StatusField) uint64 {
	c.fieldGenMu.Lock()
	defer c.fieldGenMu.Unlock()

	c.fieldGen[field].issued++

	return c.fieldGen[field].issued
}

// CompleteFieldPoll applies mut only if generation is strictly newer than
// whatever last actually applied -- poll or event -- for field. A poll for
// one field losing this race never affects any other field: an unrelated
// field's event, or an unrelated field's poll completing first, cannot
// discard this field's fresh, successful data.
func (c *DeviceConnection) CompleteFieldPoll(field StatusField, generation uint64, mut func(*DeviceStatus)) bool {
	c.fieldGenMu.Lock()

	if generation <= c.fieldGen[field].applied {
		c.fieldGenMu.Unlock()

		return false
	}

	c.fieldGen[field].applied = generation
	c.fieldGenMu.Unlock()

	c.UpdateStatus(mut)

	return true
}

// ApplyFieldEvent always applies mut -- a real-time push event is
// authoritative evidence for field -- and invalidates any poll for field
// that began before it, without touching any other field's ordering.
func (c *DeviceConnection) ApplyFieldEvent(field StatusField, mut func(*DeviceStatus)) {
	c.fieldGenMu.Lock()
	c.fieldGen[field].issued++
	c.fieldGen[field].applied = c.fieldGen[field].issued
	c.fieldGenMu.Unlock()

	c.UpdateStatus(mut)
}

// UpdateStatus atomically applies mut to a copy of the current status
// and stores the result. If another goroutine updates the status while
// mut runs, UpdateStatus retries with the newer status — so concurrent
// writers cannot silently lose each other's changes.
//
// The copy mut receives is a shallow value copy of the previous status.
// Nested pointer fields (NowPlaying, Volume, Presets, Sources, Bass, Group)
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

// ApplyPolledGroup stores a /getGroup result only if no strictly newer
// result (poll or event) has already applied. Empty groups clear the
// current claim.
func (c *DeviceConnection) ApplyPolledGroup(generation uint64, group *models.Group) bool {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	if generation <= c.groupAppliedGeneration {
		return false
	}

	c.groupAppliedGeneration = generation

	return c.replaceGroup(normalizeGroup(group), time.Time{})
}

// ApplyGroupEvent stores the newest groupUpdated event and invalidates all
// in-flight /getGroup requests, including ones that have not started yet.
// Empty teardown events clear the current claim.
func (c *DeviceConnection) ApplyGroupEvent(group *models.Group, activity time.Time) bool {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	c.groupGeneration++
	c.groupAppliedGeneration = c.groupGeneration

	return c.replaceGroup(normalizeGroup(group), activity)
}

func (c *DeviceConnection) replaceGroup(group *models.Group, activity time.Time) bool {
	changed := !models.SameGroup(c.Status().Group, group)
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
