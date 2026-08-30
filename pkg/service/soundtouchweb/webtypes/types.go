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

	status        atomic.Pointer[DeviceStatus]
	fieldRevision atomic.Uint64

	// groupMu orders polled /getGroup responses against real-time
	// groupUpdated events. Starting a newer refresh or receiving an event
	// invalidates any older in-flight poll.
	groupMu         sync.Mutex
	groupGeneration uint64

	// done is closed by Close when the device is removed from the
	// registry, signalling its background goroutines (the status poller
	// and the WebSocket reconnect loop) to exit. closeOnce keeps Close
	// idempotent.
	done      chan struct{}
	closeOnce sync.Once
}

// DeviceStatus represents the current device state
type DeviceStatus struct {
	NowPlaying         *models.NowPlaying `json:"nowPlaying,omitempty"`
	Volume             *models.Volume     `json:"volume,omitempty"`
	Presets            *models.Presets    `json:"presets,omitempty"`
	Sources            *models.Sources    `json:"sources,omitempty"`
	SourcesStale       bool               `json:"sourcesStale,omitempty"`
	SourcesReadAt      time.Time          `json:"-"`
	Bass               *models.Bass       `json:"bass,omitempty"`
	Group              *models.Group      `json:"group,omitempty"`
	IsConnected        bool               `json:"isConnected"`
	LastActivity       time.Time          `json:"lastActivity"`
	Revision           uint64             `json:"revision"`
	NowPlayingRevision uint64             `json:"nowPlayingRevision"`

	fieldRevisions deviceStatusFieldRevisions
}

type deviceStatusFieldRevisions struct {
	nowPlaying  uint64
	volume      uint64
	presets     uint64
	sources     uint64
	bass        uint64
	isConnected uint64
}

const sourceCacheTTL = 30 * time.Second

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
	return sourceCacheStatusAt(c.status.Load(), time.Now(), sourceCacheTTL)
}

// NextFieldRevision returns a connection-local monotonic revision used to
// order speaker events and REST polls before they update individual fields.
func (c *DeviceConnection) NextFieldRevision() uint64 {
	return c.fieldRevision.Add(1)
}

func sourceCacheStatusAt(status *DeviceStatus, now time.Time, ttl time.Duration) *DeviceStatus {
	stale := status.SourcesStale || status.Sources != nil && !status.SourcesReadAt.IsZero() &&
		!now.Before(status.SourcesReadAt.Add(ttl))
	if stale == status.SourcesStale {
		return status
	}

	next := *status
	next.SourcesStale = stale

	return &next
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
// concurrent changes from other goroutines. The stored Revision is derived
// from the current status rather than trusted from the caller.
func (c *DeviceConnection) SetStatus(s *DeviceStatus) {
	for {
		old := c.status.Load()
		next := *s
		next.Revision = old.Revision + 1
		fieldRevision := c.NextFieldRevision()
		next.NowPlayingRevision = fieldRevision
		next.fieldRevisions.setAll(fieldRevision)

		if c.status.CompareAndSwap(old, &next) {
			return
		}
	}
}

// UpdateStatus atomically applies mut to a copy of the current status
// and stores the result. If another goroutine updates the status while
// mut runs, UpdateStatus retries with the newer status — so concurrent
// writers cannot silently lose each other's changes. Every successful store
// advances the public Revision exactly once.
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
		next.Revision = old.Revision + 1

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

// MergeNowPlaying applies a value unless a newer event or poll already wrote
// the field.
func (s *DeviceStatus) MergeNowPlaying(nowPlaying *models.NowPlaying, revision uint64) bool {
	if !acceptFieldRevision(&s.fieldRevisions.nowPlaying, revision) {
		return false
	}

	s.NowPlaying = nowPlaying
	s.NowPlayingRevision = revision

	return true
}

// MergeVolume applies a value unless a newer event or poll already wrote the
// field.
func (s *DeviceStatus) MergeVolume(volume *models.Volume, revision uint64) bool {
	if !acceptFieldRevision(&s.fieldRevisions.volume, revision) {
		return false
	}

	s.Volume = volume

	return true
}

// MergePresets applies a value unless a newer event or poll already wrote the
// field.
func (s *DeviceStatus) MergePresets(presets *models.Presets, revision uint64) bool {
	if !acceptFieldRevision(&s.fieldRevisions.presets, revision) {
		return false
	}

	s.Presets = presets

	return true
}

// MergeSources applies a successful source read unless a newer refresh already
// wrote the field.
func (s *DeviceStatus) MergeSources(sources *models.Sources, readAt time.Time, revision uint64) bool {
	if !acceptFieldRevision(&s.fieldRevisions.sources, revision) {
		return false
	}

	s.Sources = sources
	s.SourcesReadAt = readAt
	s.SourcesStale = false

	return true
}

// MergeSourcesFailure records an unsuccessful source refresh unless a newer
// refresh already completed. The last successful inventory remains available
// for display, but is immediately marked stale so it cannot be acted on.
func (s *DeviceStatus) MergeSourcesFailure(revision uint64) bool {
	if !acceptFieldRevision(&s.fieldRevisions.sources, revision) {
		return false
	}

	s.SourcesStale = true

	return true
}

// MergeBass applies a value unless a newer poll already wrote the field.
func (s *DeviceStatus) MergeBass(bass *models.Bass, revision uint64) bool {
	if !acceptFieldRevision(&s.fieldRevisions.bass, revision) {
		return false
	}

	s.Bass = bass

	return true
}

// MergeIsConnected applies a value unless a newer connection event or poll
// already wrote the field.
func (s *DeviceStatus) MergeIsConnected(isConnected bool, revision uint64) bool {
	if !acceptFieldRevision(&s.fieldRevisions.isConnected, revision) {
		return false
	}

	s.IsConnected = isConnected

	return true
}

func acceptFieldRevision(current *uint64, next uint64) bool {
	if next < *current {
		return false
	}

	*current = next

	return true
}

func (r *deviceStatusFieldRevisions) setAll(revision uint64) {
	r.nowPlaying = revision
	r.volume = revision
	r.presets = revision
	r.sources = revision
	r.bass = revision
	r.isConnected = revision
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

// SourceRequest represents an exact source selection request.
type SourceRequest struct {
	Source  string `json:"source"`
	Account string `json:"account"`
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
