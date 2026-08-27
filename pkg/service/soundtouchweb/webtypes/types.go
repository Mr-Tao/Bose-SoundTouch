// Package webtypes contains type definitions for the SoundTouch web UI.
package webtypes

import (
	"errors"
	"fmt"
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
	Client *client.Client
	// WebSocket is retained for source compatibility with callers that build
	// DeviceConnection values directly. New concurrent code should use
	// CurrentWebSocket and SetWebSocket.
	WebSocket *client.WebSocketClient
	// DeviceInfo is the immutable discovery snapshot. Use Info for player-facing
	// output so later nameUpdated events are reflected without racing readers.
	DeviceInfo *models.DeviceInfo
	LastSeen   time.Time

	deviceName atomic.Pointer[string]
	status     atomic.Pointer[DeviceStatus]

	webSocketMu sync.RWMutex

	webSocketLoopRunning atomic.Bool

	nameMu     sync.Mutex
	nameGen    uint64
	volumeMu   sync.Mutex
	volumeGen  uint64
	balanceMu  sync.Mutex
	balanceGen uint64
	healthMu   sync.Mutex

	bassCapabilitiesMu     sync.Mutex
	bassCapabilitiesFlight *bassCapabilitiesFlight

	balanceOperationMu sync.Mutex
	balanceWriteMu     sync.Mutex

	nextPollGeneration  uint64
	lastPollGeneration  uint64
	lastHTTPSuccess     time.Time
	consecutiveFailures int
	speakerEventGen     uint64
	pollEventGen        map[uint64]uint64

	// groupMu orders polled /getGroup responses against real-time
	// groupUpdated events. Starting a newer refresh or receiving an event
	// invalidates any older in-flight poll.
	groupMu                  sync.Mutex
	groupGeneration          uint64
	confirmedGroupGeneration uint64

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
	NowPlaying             *models.NowPlaying       `json:"nowPlaying,omitempty"`
	Volume                 *models.Volume           `json:"volume,omitempty"`
	Balance                *models.Balance          `json:"balance,omitempty"`
	Presets                *models.Presets          `json:"presets,omitempty"`
	Sources                *models.Sources          `json:"sources,omitempty"`
	Bass                   *models.Bass             `json:"bass,omitempty"`
	BassCapabilities       *models.BassCapabilities `json:"bassCapabilities,omitempty"`
	Group                  *models.Group            `json:"group,omitempty"`
	Zone                   *models.ZoneInfo         `json:"zone,omitempty"`
	Connectivity           Connectivity             `json:"connectivity"`
	HTTPReachable          bool                     `json:"httpReachable"`
	WebSocketConnected     bool                     `json:"webSocketConnected"`
	SpeakerConnectionState *SpeakerConnectionState  `json:"speakerConnectionState,omitempty"`
	IsConnected            bool                     `json:"isConnected"`
	LastActivity           time.Time                `json:"lastActivity"`
}

// Connectivity is the player's view of HTTP reachability. It deliberately
// excludes both the event WebSocket transport and the network state reported
// by the speaker itself: either can flap while the control API remains usable.
type Connectivity string

const (
	// ConnectivityOnline means the latest HTTP poll reached the speaker.
	ConnectivityOnline Connectivity = "online"
	// ConnectivityStale means recent HTTP probes failed within the grace period.
	ConnectivityStale Connectivity = "stale"
	// ConnectivityOffline means repeated HTTP failures exceeded the grace period.
	ConnectivityOffline Connectivity = "offline"
)

const (
	offlineFailureThreshold = 2
	offlineGracePeriod      = 60 * time.Second
)

// SpeakerConnectionState is the network state reported by a SoundTouch
// connectionStateUpdated event. It is diagnostic data, not proof that the
// player can currently reach the speaker's HTTP API.
type SpeakerConnectionState struct {
	State  string `json:"state"`
	Signal string `json:"signal,omitempty"`
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
		Connectivity: ConnectivityOffline,
		IsConnected:  false,
		LastActivity: time.Now(),
	})

	if info != nil {
		conn.storeDeviceName(info.Name)
	}

	return conn
}

func (c *DeviceConnection) storeDeviceName(name string) {
	c.deviceName.Store(&name)
}

// BeginNameRefresh starts a new generation for an asynchronous /name request.
// Only the latest poll may later update the display name.
func (c *DeviceConnection) BeginNameRefresh() uint64 {
	c.nameMu.Lock()
	defer c.nameMu.Unlock()

	c.nameGen++

	return c.nameGen
}

// ApplyPolledName stores a /name result unless a newer poll or nameUpdated
// event superseded it.
func (c *DeviceConnection) ApplyPolledName(generation uint64, name string) bool {
	c.nameMu.Lock()
	defer c.nameMu.Unlock()

	if generation != c.nameGen {
		return false
	}

	c.storeDeviceName(name)

	return true
}

// ApplyNameEvent stores the newest nameUpdated event and invalidates all
// in-flight /name requests.
func (c *DeviceConnection) ApplyNameEvent(name string) {
	c.nameMu.Lock()
	defer c.nameMu.Unlock()

	c.nameGen++
	c.storeDeviceName(name)
}

// BeginVolumeRefresh starts a field-specific generation for an asynchronous
// /volume readback. Unlike a full HTTP status poll, an unrelated speaker event
// must not invalidate a confirmed volume value.
func (c *DeviceConnection) BeginVolumeRefresh() uint64 {
	c.volumeMu.Lock()
	defer c.volumeMu.Unlock()

	c.volumeGen++

	return c.volumeGen
}

// ApplyPolledVolume stores a /volume result unless a newer volume readback or
// volumeUpdated event superseded it.
func (c *DeviceConnection) ApplyPolledVolume(generation uint64, volume *models.Volume) bool {
	c.volumeMu.Lock()
	defer c.volumeMu.Unlock()

	if generation != c.volumeGen {
		return false
	}

	c.UpdateStatus(func(status *DeviceStatus) {
		status.Volume = volume
	})

	return true
}

// ApplyVolumeEvent stores the newest volumeUpdated event and invalidates all
// in-flight /volume readbacks. It also participates in full-poll ordering.
func (c *DeviceConnection) ApplyVolumeEvent(volume *models.Volume, activity time.Time) {
	c.volumeMu.Lock()
	defer c.volumeMu.Unlock()

	c.volumeGen++
	c.ApplySpeakerEvent(func(status *DeviceStatus) {
		status.Volume = volume
		status.LastActivity = activity
	})
}

// WithBalanceOperation serializes balance endpoint traffic for this physical
// speaker. Stereo-balance writes and periodic readbacks use this seam so only
// the confirmed group master is accessed and operations cannot overlap.
func (c *DeviceConnection) WithBalanceOperation(operation func()) {
	c.balanceOperationMu.Lock()
	defer c.balanceOperationMu.Unlock()

	operation()
}

// WithBalanceWriteFence linearizes balance write initiation with group and
// registry invalidation without delaying invalidation across GET readback.
func (c *DeviceConnection) WithBalanceWriteFence(operation func()) {
	c.balanceWriteMu.Lock()
	defer c.balanceWriteMu.Unlock()

	operation()
}

// BalanceRefresh ties a /balance readback to one confirmed group generation.
// Group and Balance are immutable snapshots and must not be modified.
type BalanceRefresh struct {
	Group   *models.Group
	Balance *models.Balance

	groupGeneration   uint64
	balanceGeneration uint64
}

// BeginBalanceRefresh snapshots the latest confirmed group and balance
// capability. An in-flight or failed /getGroup refresh makes balance access
// unsafe until a newer group response is confirmed.
func (c *DeviceConnection) BeginBalanceRefresh() (BalanceRefresh, bool) {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	c.balanceMu.Lock()
	defer c.balanceMu.Unlock()

	if c.groupGeneration != c.confirmedGroupGeneration {
		return BalanceRefresh{}, false
	}

	c.balanceGen++
	status := c.Status()

	return BalanceRefresh{
		Group:             status.Group,
		Balance:           status.Balance,
		groupGeneration:   c.groupGeneration,
		balanceGeneration: c.balanceGen,
	}, true
}

// BalanceRefreshCurrent reports whether neither topology nor a newer balance
// operation has superseded refresh.
func (c *DeviceConnection) BalanceRefreshCurrent(refresh BalanceRefresh) bool {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	c.balanceMu.Lock()
	defer c.balanceMu.Unlock()

	return c.balanceRefreshCurrentLocked(refresh)
}

// ApplyBalanceReadback stores a confirmed /balance response only while both
// the group and balance generations captured by refresh remain current.
func (c *DeviceConnection) ApplyBalanceReadback(refresh BalanceRefresh, balance *models.Balance) bool {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	c.balanceMu.Lock()
	defer c.balanceMu.Unlock()

	if !c.balanceRefreshCurrentLocked(refresh) {
		return false
	}

	c.UpdateStatus(func(status *DeviceStatus) {
		status.Balance = balance
	})

	return true
}

func (c *DeviceConnection) balanceRefreshCurrentLocked(refresh BalanceRefresh) bool {
	currentGroupGeneration := c.groupGeneration

	return currentGroupGeneration == c.confirmedGroupGeneration &&
		refresh.groupGeneration == currentGroupGeneration &&
		refresh.balanceGeneration == c.balanceGen &&
		reflect.DeepEqual(refresh.Group, c.Status().Group)
}

// BassCapabilitiesFetchOutcome describes whether EnsureBassCapabilities made a
// fresh request, reused the cache, or failed to obtain a valid response.
type BassCapabilitiesFetchOutcome uint8

const (
	// BassCapabilitiesFetchFailed means no valid capability response was obtained.
	BassCapabilitiesFetchFailed BassCapabilitiesFetchOutcome = iota
	// BassCapabilitiesCacheHit means a previously validated response was reused.
	BassCapabilitiesCacheHit
	// BassCapabilitiesFetched means a fresh valid capability response was stored.
	BassCapabilitiesFetched
)

type bassCapabilitiesFlight struct {
	done    chan struct{}
	outcome BassCapabilitiesFetchOutcome
	err     error
}

// EnsureBassCapabilities retains the first successful valid capability
// response for this physical connection. Calls that arrive during a failed
// fetch share that failure; a later call starts a new attempt.
func (c *DeviceConnection) EnsureBassCapabilities(
	fetch func() (*models.BassCapabilities, error),
) (BassCapabilitiesFetchOutcome, error) {
	c.bassCapabilitiesMu.Lock()

	if capabilities := c.Status().BassCapabilities; capabilities != nil {
		if err := capabilities.Validate(); err == nil {
			c.bassCapabilitiesMu.Unlock()
			return BassCapabilitiesCacheHit, nil
		}

		c.UpdateStatus(func(status *DeviceStatus) {
			status.BassCapabilities = nil
		})
	}

	if flight := c.bassCapabilitiesFlight; flight != nil {
		c.bassCapabilitiesMu.Unlock()
		<-flight.done

		if flight.outcome == BassCapabilitiesFetchFailed {
			return BassCapabilitiesFetchFailed, flight.err
		}

		return BassCapabilitiesCacheHit, nil
	}

	flight := &bassCapabilitiesFlight{done: make(chan struct{})}
	c.bassCapabilitiesFlight = flight
	c.bassCapabilitiesMu.Unlock()

	capabilities, err := fetch()
	if err == nil {
		if capabilities == nil {
			err = errors.New("bass capabilities response is nil")
		} else if validationErr := capabilities.Validate(); validationErr != nil {
			err = fmt.Errorf("invalid bass capabilities: %w", validationErr)
		}
	}

	if err == nil {
		c.UpdateStatus(func(status *DeviceStatus) {
			status.BassCapabilities = capabilities
		})
	}

	flightOutcome := BassCapabilitiesFetched
	if err != nil {
		flightOutcome = BassCapabilitiesFetchFailed
	}

	c.bassCapabilitiesMu.Lock()
	flight.outcome = flightOutcome
	flight.err = err
	c.bassCapabilitiesFlight = nil

	close(flight.done)
	c.bassCapabilitiesMu.Unlock()

	if err != nil {
		return BassCapabilitiesFetchFailed, err
	}

	return BassCapabilitiesFetched, nil
}

// Info returns a read-only metadata snapshot with the latest device name.
func (c *DeviceConnection) Info() *models.DeviceInfo {
	if c.DeviceInfo == nil {
		return nil
	}

	name := c.deviceName.Load()
	if name == nil || *name == c.DeviceInfo.Name {
		return c.DeviceInfo
	}

	info := *c.DeviceInfo
	info.Name = *name

	return &info
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

		if ws := c.CurrentWebSocket(); ws != nil {
			_ = ws.Close()
		}
	})
}

// CurrentWebSocket returns the currently connected device event transport, if
// any. It is the concurrency-safe counterpart to the compatibility field.
func (c *DeviceConnection) CurrentWebSocket() *client.WebSocketClient {
	c.webSocketMu.RLock()
	defer c.webSocketMu.RUnlock()

	return c.WebSocket
}

// SetWebSocket stores the current device event transport. Passing nil clears
// it. HTTP handlers inspect the pointer concurrently with the reconnect loop,
// so internal code pairs this method with CurrentWebSocket.
func (c *DeviceConnection) SetWebSocket(ws *client.WebSocketClient) {
	c.webSocketMu.Lock()
	c.WebSocket = ws
	c.webSocketMu.Unlock()
}

// TryStartWebSocketLoop claims ownership of the one reconnect loop allowed for
// this device. The owner must call FinishWebSocketLoop when it exits.
func (c *DeviceConnection) TryStartWebSocketLoop() bool {
	return c.webSocketLoopRunning.CompareAndSwap(false, true)
}

// FinishWebSocketLoop releases reconnect-loop ownership.
func (c *DeviceConnection) FinishWebSocketLoop() {
	c.webSocketLoopRunning.Store(false)
}

// BeginHTTPPoll returns a monotonically increasing generation for a status
// poll. CompleteHTTPPoll uses it to prevent an older, slower result from
// overwriting a newer one.
func (c *DeviceConnection) BeginHTTPPoll() uint64 {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	c.nextPollGeneration++
	if c.pollEventGen == nil {
		c.pollEventGen = make(map[uint64]uint64)
	}

	c.pollEventGen[c.nextPollGeneration] = c.speakerEventGen

	return c.nextPollGeneration
}

// ApplySpeakerEvent serializes a real-time speaker event against HTTP poll
// completion. A poll that began before this event may still update health, but
// cannot merge older payload fields over the event.
func (c *DeviceConnection) ApplySpeakerEvent(mut func(*DeviceStatus)) {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	c.speakerEventGen++
	c.UpdateStatus(mut)
}

// CompleteHTTPPoll records the outcome of one HTTP status poll and applies its
// successfully fetched fields through merge. It returns false when a newer
// poll has already completed and this result was therefore discarded.
func (c *DeviceConnection) CompleteHTTPPoll(
	generation uint64,
	success bool,
	at time.Time,
	merge func(*DeviceStatus),
) bool {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	pollEventGeneration, knownGeneration := c.pollEventGen[generation]
	delete(c.pollEventGen, generation)

	if generation <= c.lastPollGeneration {
		return false
	}

	c.lastPollGeneration = generation
	for olderGeneration := range c.pollEventGen {
		if olderGeneration < generation {
			delete(c.pollEventGen, olderGeneration)
		}
	}

	connectivity := ConnectivityStale

	if success {
		c.lastHTTPSuccess = at
		c.consecutiveFailures = 0
		connectivity = ConnectivityOnline
	} else {
		c.consecutiveFailures++
		if c.consecutiveFailures >= offlineFailureThreshold &&
			!c.lastHTTPSuccess.IsZero() &&
			at.Sub(c.lastHTTPSuccess) >= offlineGracePeriod {
			connectivity = ConnectivityOffline
		}
	}

	c.UpdateStatus(func(status *DeviceStatus) {
		if merge != nil && knownGeneration && pollEventGeneration == c.speakerEventGen {
			merge(status)
		}

		status.Connectivity = connectivity
		status.HTTPReachable = success

		status.IsConnected = connectivity != ConnectivityOffline
		if success {
			status.LastActivity = at
		}
	})

	return true
}

// MarkHTTPSuccess records a successful out-of-band HTTP request such as the
// /info request used while adding a device.
func (c *DeviceConnection) MarkHTTPSuccess(at time.Time) {
	generation := c.BeginHTTPPoll()
	c.CompleteHTTPPoll(generation, true, at, nil)
}

// SetStatus atomically replaces the entire status. Use sparingly —
// UpdateStatus is the preferred entry point because it preserves
// concurrent changes from other goroutines.
func (c *DeviceConnection) SetStatus(s *DeviceStatus) {
	c.status.Store(s)
}

// UpdateStatus atomically applies mut to a copy of the current status
// and stores the result. If another goroutine updates the status while
// mut runs, UpdateStatus retries with the newer status — so concurrent
// writers cannot silently lose each other's changes.
//
// The copy mut receives is a shallow value copy of the previous status.
// Nested pointer fields (NowPlaying, Volume, Balance, Presets, Sources, Bass,
// BassCapabilities, Group, Zone)
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
	var generation uint64

	c.WithBalanceWriteFence(func() {
		c.groupMu.Lock()
		defer c.groupMu.Unlock()

		c.balanceMu.Lock()
		defer c.balanceMu.Unlock()

		c.groupGeneration++
		c.balanceGen++
		c.UpdateStatus(func(status *DeviceStatus) {
			status.Balance = nil
		})
		generation = c.groupGeneration
	})

	return generation
}

// ApplyPolledGroup stores a /getGroup result only when no newer poll or
// groupUpdated event superseded it. It reports whether the response was
// accepted; empty groups clear the current claim.
func (c *DeviceConnection) ApplyPolledGroup(generation uint64, group *models.Group) bool {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	c.balanceMu.Lock()
	defer c.balanceMu.Unlock()

	if generation != c.groupGeneration {
		return false
	}

	c.confirmedGroupGeneration = generation

	c.replaceGroup(normalizeGroup(group), time.Time{})

	return true
}

// ApplyGroupEvent stores the newest groupUpdated event and invalidates all
// in-flight /getGroup requests. Empty teardown events clear the current claim.
func (c *DeviceConnection) ApplyGroupEvent(group *models.Group, activity time.Time) bool {
	changed := false

	c.WithBalanceWriteFence(func() {
		c.groupMu.Lock()
		defer c.groupMu.Unlock()

		c.balanceMu.Lock()
		defer c.balanceMu.Unlock()

		c.groupGeneration++
		c.confirmedGroupGeneration = c.groupGeneration
		c.balanceGen++
		changed = c.replaceGroup(normalizeGroup(group), activity)
	})

	return changed
}

func (c *DeviceConnection) replaceGroup(group *models.Group, activity time.Time) bool {
	changed := !reflect.DeepEqual(c.Status().Group, group)
	c.UpdateStatus(func(s *DeviceStatus) {
		s.Group = group

		s.Balance = nil
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
// master and no newer refresh superseded it. An empty response from the
// queried device authoritatively clears the cached zone; member responses and
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

	c.replaceZone(normalizeZone(zone))

	return true
}

func (c *DeviceConnection) replaceZone(zone *models.ZoneInfo) bool {
	changed := !reflect.DeepEqual(c.Status().Zone, zone)
	c.UpdateStatus(func(status *DeviceStatus) {
		status.Zone = zone
	})

	return changed
}

func normalizeZone(zone *models.ZoneInfo) *models.ZoneInfo {
	if zone == nil || zone.IsStandalone() {
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
