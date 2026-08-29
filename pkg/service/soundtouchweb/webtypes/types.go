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
	bassMu     sync.Mutex
	bassGen    uint64
	balanceMu  sync.Mutex
	balanceGen uint64
	healthMu   sync.Mutex

	bassCapabilitiesMu     sync.Mutex
	bassCapabilitiesFlight *bassCapabilitiesFlight

	volumeOperationMu  sync.Mutex
	bassOperationMu    sync.Mutex
	balanceOperationMu sync.Mutex
	balanceWriteMu     sync.Mutex
	nowPlayingMu       sync.Mutex
	zoneWriteMu        sync.RWMutex

	nextPollGeneration  uint64
	lastPollGeneration  uint64
	httpReachable       bool
	consecutiveFailures int
	speakerEventGen     uint64
	pollEventGen        map[uint64]uint64

	nextEventStreamGeneration  uint64
	lastEventStreamGeneration  uint64
	eventStreamConnected       bool
	lastDirectSuccess          time.Time
	speakerConnectionKnown     bool
	speakerConnectionConnected bool
	speakerConnectionObserved  time.Time

	// groupMu orders polled /getGroup responses against real-time
	// groupUpdated events. Starting a newer refresh or receiving an event
	// invalidates any older in-flight poll.
	groupMu                  sync.Mutex
	groupGeneration          uint64
	confirmedGroupGeneration uint64

	// zoneMu protects the last topology confirmed by a zone master. Member
	// responses and failed refreshes must not dissolve a logical zone.
	zoneMu                          sync.Mutex
	zoneGeneration                  uint64
	confirmedZoneGeneration         uint64
	pendingZoneProjectionGeneration uint64

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
	BalanceRevision        uint64                   `json:"balanceRevision"`
	Presets                *models.Presets          `json:"presets,omitempty"`
	Sources                *models.Sources          `json:"sources,omitempty"`
	Bass                   *models.Bass             `json:"bass,omitempty"`
	BassRevision           uint64                   `json:"bassRevision"`
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

// Connectivity is the player's aggregate view of device HTTP reachability,
// the event WebSocket transport, and the network state reported by the speaker.
// A single transport flap cannot make a still-reachable speaker offline.
type Connectivity string

const (
	// ConnectivityOnline means at least one direct player-to-speaker path is live.
	ConnectivityOnline Connectivity = "online"
	// ConnectivityStale means direct paths are currently inconclusive or recently failed.
	ConnectivityStale Connectivity = "stale"
	// ConnectivityOffline means all direct paths failed beyond the grace period.
	ConnectivityOffline Connectivity = "offline"
)

const (
	offlineFailureThreshold = 2
	offlineGracePeriod      = 60 * time.Second
)

// SpeakerConnectionState is the network state reported by a SoundTouch
// connectionStateUpdated event. It is supporting evidence and cannot override
// a current HTTP or event-stream success by itself.
type SpeakerConnectionState struct {
	State  string `json:"state"`
	Signal string `json:"signal,omitempty"`
}

type connectivityProjection struct {
	connectivity       Connectivity
	httpReachable      bool
	webSocketConnected bool
	isConnected        bool
}

func projectedConnectivity(status *DeviceStatus) connectivityProjection {
	return connectivityProjection{
		connectivity:       status.Connectivity,
		httpReachable:      status.HTTPReachable,
		webSocketConnected: status.WebSocketConnected,
		isConnected:        status.IsConnected,
	}
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
	c.ApplyNameEventChanged(name)
}

// ApplyNameEventChanged additionally reports whether the projected name changed.
func (c *DeviceConnection) ApplyNameEventChanged(name string) bool {
	c.nameMu.Lock()
	defer c.nameMu.Unlock()

	current := c.deviceName.Load()
	changed := current == nil || *current != name
	c.nameGen++
	c.storeDeviceName(name)

	return changed
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
	c.ApplyVolumeEventChanged(volume, activity)
}

// ApplyVolumeEventChanged additionally reports whether the browser-visible
// projection changed.
func (c *DeviceConnection) ApplyVolumeEventChanged(volume *models.Volume, activity time.Time) bool {
	c.volumeMu.Lock()
	defer c.volumeMu.Unlock()

	changed := false
	c.volumeGen++
	connectivityChanged := c.ApplySpeakerEventChanged(func(status *DeviceStatus) {
		changed = !reflect.DeepEqual(status.Volume, volume)
		status.Volume = volume
		status.LastActivity = activity
	})

	return changed || connectivityChanged
}

// ApplyPresetEvent stores the newest presetsUpdated event in the same ordering
// domain as full status polls and reports whether its projection changed.
func (c *DeviceConnection) ApplyPresetEvent(presets *models.Presets, activity time.Time) bool {
	changed := false
	connectivityChanged := c.ApplySpeakerEventChanged(func(status *DeviceStatus) {
		changed = !reflect.DeepEqual(status.Presets, presets)
		status.Presets = presets
		status.LastActivity = activity
	})

	return changed || connectivityChanged
}

// WithVolumeOperation serializes a volume write and its mandatory readback for
// one physical speaker.
func (c *DeviceConnection) WithVolumeOperation(operation func()) {
	c.volumeOperationMu.Lock()
	defer c.volumeOperationMu.Unlock()

	operation()
}

// WithBassOperation serializes bass polling, writes, and events for this
// physical speaker. The field generation still fences a fetched value until
// it is committed after slower unrelated status reads finish.
func (c *DeviceConnection) WithBassOperation(operation func()) {
	c.bassOperationMu.Lock()
	defer c.bassOperationMu.Unlock()

	operation()
}

// BeginBassRefresh starts a field-specific generation for one /bass readback.
func (c *DeviceConnection) BeginBassRefresh() uint64 {
	c.bassMu.Lock()
	defer c.bassMu.Unlock()

	c.bassGen++

	return c.bassGen
}

// ApplyPolledBass stores a /bass readback unless a newer readback or
// bassUpdated event superseded it.
func (c *DeviceConnection) ApplyPolledBass(generation uint64, bass *models.Bass) bool {
	_, applied := c.ApplyPolledBassWithRevision(generation, bass)

	return applied
}

// ApplyPolledBassWithRevision stores a confirmed /bass response and returns
// the revision assigned to that exact accepted readback.
func (c *DeviceConnection) ApplyPolledBassWithRevision(generation uint64, bass *models.Bass) (uint64, bool) {
	c.bassMu.Lock()
	defer c.bassMu.Unlock()

	if generation != c.bassGen {
		return 0, false
	}

	var revision uint64

	c.UpdateStatus(func(status *DeviceStatus) {
		status.Bass = bass
		status.BassRevision++
		revision = status.BassRevision
	})

	return revision, true
}

// ApplyBassEvent stores the newest bassUpdated event and invalidates all
// in-flight /bass readbacks. It also participates in full-poll ordering.
func (c *DeviceConnection) ApplyBassEvent(bass *models.Bass, activity time.Time) {
	c.ApplyBassEventChanged(bass, activity)
}

// ApplyBassEventChanged additionally reports whether the browser-visible
// projection changed.
func (c *DeviceConnection) ApplyBassEventChanged(bass *models.Bass, activity time.Time) bool {
	c.bassMu.Lock()
	defer c.bassMu.Unlock()

	changed := false
	c.bassGen++
	connectivityChanged := c.ApplySpeakerEventChanged(func(status *DeviceStatus) {
		changed = !reflect.DeepEqual(status.Bass, bass)
		status.Bass = bass
		status.BassRevision++
		status.LastActivity = activity
	})

	return changed || connectivityChanged
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

// WithGroupWriteFence linearizes any group-owned control write with group
// refresh/event invalidation. It shares the balance fence because both volume
// and balance authority depend on the same physical group master.
func (c *DeviceConnection) WithGroupWriteFence(operation func()) {
	c.balanceWriteMu.Lock()
	defer c.balanceWriteMu.Unlock()

	operation()
}

// WithZoneWriteFence linearizes a zone-owned control write with the start of
// a newer master /getZone refresh. Multiple member writes may share the fence;
// the fence is released before network readback so a topology change can
// invalidate that readback.
func (c *DeviceConnection) WithZoneWriteFence(operation func()) {
	c.zoneWriteMu.RLock()
	defer c.zoneWriteMu.RUnlock()

	operation()
}

// GroupTopology is an immutable snapshot of one confirmed group generation.
type GroupTopology struct {
	Group *models.Group

	generation uint64
}

// ZoneTopology is an immutable snapshot of one confirmed zone generation.
type ZoneTopology struct {
	Zone *models.ZoneInfo

	generation uint64
}

// SnapshotGroupTopology captures the currently confirmed group ownership.
// An in-flight or failed /getGroup refresh makes group-owned writes unsafe.
func (c *DeviceConnection) SnapshotGroupTopology() (GroupTopology, bool) {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	if c.groupGeneration != c.confirmedGroupGeneration {
		return GroupTopology{}, false
	}

	return GroupTopology{
		Group:      c.Status().Group,
		generation: c.groupGeneration,
	}, true
}

// GroupTopologyCurrent reports whether group ownership still matches the
// confirmed generation captured before a control operation.
func (c *DeviceConnection) GroupTopologyCurrent(topology GroupTopology) bool {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	return c.groupGeneration == c.confirmedGroupGeneration &&
		topology.generation == c.groupGeneration &&
		reflect.DeepEqual(topology.Group, c.Status().Group)
}

// SnapshotZoneTopology captures the last topology accepted from this zone
// master. An in-flight or failed /getZone refresh makes zone-owned writes
// unsafe until a newer response is confirmed.
func (c *DeviceConnection) SnapshotZoneTopology() (ZoneTopology, bool) {
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()

	if c.zoneGeneration != c.confirmedZoneGeneration {
		return ZoneTopology{}, false
	}

	return ZoneTopology{
		Zone:       c.Status().Zone,
		generation: c.zoneGeneration,
	}, true
}

// ZoneTopologyCurrent reports whether the confirmed zone still matches the
// generation captured before a control operation.
func (c *DeviceConnection) ZoneTopologyCurrent(topology ZoneTopology) bool {
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()

	return c.zoneGeneration == c.confirmedZoneGeneration &&
		topology.generation == c.zoneGeneration &&
		reflect.DeepEqual(topology.Zone, c.Status().Zone)
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
	_, applied := c.ApplyBalanceReadbackWithRevision(refresh, balance)

	return applied
}

// ApplyBalanceReadbackWithRevision stores a confirmed /balance response and
// returns the revision assigned to that exact accepted readback.
func (c *DeviceConnection) ApplyBalanceReadbackWithRevision(
	refresh BalanceRefresh,
	balance *models.Balance,
) (uint64, bool) {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()

	c.balanceMu.Lock()
	defer c.balanceMu.Unlock()

	if !c.balanceRefreshCurrentLocked(refresh) {
		return 0, false
	}

	var revision uint64

	c.UpdateStatus(func(status *DeviceStatus) {
		status.Balance = balance
		status.BalanceRevision++
		revision = status.BalanceRevision
	})

	return revision, true
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
// completion. A poll that began before this event may still update its own HTTP
// input, but cannot merge older payload fields or demote the newer live event
// stream observation.
func (c *DeviceConnection) ApplySpeakerEvent(mut func(*DeviceStatus)) {
	c.ApplySpeakerEventChanged(mut)
}

// ApplySpeakerEventChanged additionally reports whether the browser-visible
// connectivity projection changed.
func (c *DeviceConnection) ApplySpeakerEventChanged(mut func(*DeviceStatus)) bool {
	return c.ApplySpeakerEventAtChanged(time.Now(), mut)
}

// ApplySpeakerEventAt is the deterministic-time form used by event handlers
// and tests. Receiving any event proves that the event stream is currently
// usable, irrespective of the event's payload.
func (c *DeviceConnection) ApplySpeakerEventAt(at time.Time, mut func(*DeviceStatus)) {
	c.ApplySpeakerEventAtChanged(at, mut)
}

// ApplySpeakerEventAtChanged is the deterministic-time form that additionally
// reports whether the browser-visible connectivity projection changed.
func (c *DeviceConnection) ApplySpeakerEventAtChanged(at time.Time, mut func(*DeviceStatus)) bool {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	c.speakerEventGen++
	c.markEventStreamActivityLocked(at)

	changed := false

	c.UpdateStatus(func(status *DeviceStatus) {
		previousConnectivity := projectedConnectivity(status)

		if mut != nil {
			mut(status)
		}

		c.applyConnectivityLocked(status, at)
		changed = previousConnectivity != projectedConnectivity(status)
	})

	return changed
}

// ApplyNowPlayingEvent stores a now-playing event in the same ordering domain
// as full HTTP polls and returns the source it replaced. Callers can therefore
// detect a real transition without a read-before-update race.
func (c *DeviceConnection) ApplyNowPlayingEvent(
	nowPlaying *models.NowPlaying,
	at time.Time,
) string {
	previousSource, _ := c.ApplyNowPlayingEventChanged(nowPlaying, at)

	return previousSource
}

// ApplyNowPlayingEventChanged stores a now-playing event and additionally
// reports whether its browser-visible projection changed.
func (c *DeviceConnection) ApplyNowPlayingEventChanged(
	nowPlaying *models.NowPlaying,
	at time.Time,
) (string, bool) {
	return c.ApplyNowPlayingEventChangedBefore(nowPlaying, at, nil)
}

// ApplyNowPlayingEventChangedBefore serializes a pre-apply ordering barrier
// with full-poll now-playing merges. The callback may reserve related topology
// generations before the event becomes visible to browser projection.
func (c *DeviceConnection) ApplyNowPlayingEventChangedBefore(
	nowPlaying *models.NowPlaying,
	at time.Time,
	beforeApply func(previousSource string),
) (string, bool) {
	c.nowPlayingMu.Lock()
	defer c.nowPlayingMu.Unlock()

	previousSource := ""
	if status := c.Status(); status != nil && status.NowPlaying != nil {
		previousSource = status.NowPlaying.Source
	}

	if beforeApply != nil {
		beforeApply(previousSource)
	}

	return c.applyNowPlayingEventChanged(nowPlaying, at)
}

func (c *DeviceConnection) applyNowPlayingEventChanged(
	nowPlaying *models.NowPlaying,
	at time.Time,
) (string, bool) {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	c.speakerEventGen++
	c.markEventStreamActivityLocked(at)

	previousSource := ""
	changed := false

	c.UpdateStatus(func(status *DeviceStatus) {
		previousConnectivity := projectedConnectivity(status)

		if status.NowPlaying != nil {
			previousSource = status.NowPlaying.Source
		}

		changed = !reflect.DeepEqual(status.NowPlaying, nowPlaying)
		status.NowPlaying = nowPlaying
		status.LastActivity = at
		c.applyConnectivityLocked(status, at)
		changed = changed || previousConnectivity != projectedConnectivity(status)
	})

	return previousSource, changed
}

// ApplySpeakerConnectionEvent stores a speaker-reported connection state as a
// separate supporting input. The event itself is also direct evidence that the
// event stream is live. Unknown values remain diagnostic only.
func (c *DeviceConnection) ApplySpeakerConnectionEvent(state SpeakerConnectionState, at time.Time) {
	c.ApplySpeakerConnectionEventChanged(state, at)
}

// ApplySpeakerConnectionEventChanged additionally reports whether the
// browser-visible state changed.
func (c *DeviceConnection) ApplySpeakerConnectionEventChanged(
	state SpeakerConnectionState,
	at time.Time,
) bool {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	c.speakerEventGen++
	c.markEventStreamActivityLocked(at)
	c.speakerConnectionObserved = at

	switch strings.ToUpper(strings.TrimSpace(state.State)) {
	case string(models.ConnectionStateConnected):
		c.speakerConnectionKnown = true
		c.speakerConnectionConnected = true
	case string(models.ConnectionStateDisconnected):
		c.speakerConnectionKnown = true
		c.speakerConnectionConnected = false
	default:
		c.speakerConnectionKnown = false
		c.speakerConnectionConnected = false
	}

	changed := false

	c.UpdateStatus(func(status *DeviceStatus) {
		previousConnectivity := projectedConnectivity(status)

		reported := state
		changed = !reflect.DeepEqual(status.SpeakerConnectionState, &reported)
		status.SpeakerConnectionState = &reported
		status.LastActivity = at
		c.applyConnectivityLocked(status, at)
		changed = changed || previousConnectivity != projectedConnectivity(status)
	})

	return changed
}

// BeginEventStreamObservation reserves an ordering generation before the
// caller samples WebSocketClient.IsConnected. A speaker event arriving during
// that sample receives a newer generation and prevents the stale sample from
// overwriting it.
func (c *DeviceConnection) BeginEventStreamObservation() uint64 {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	c.nextEventStreamGeneration++

	return c.nextEventStreamGeneration
}

// CompleteEventStreamObservation applies a sampled transport state unless a
// newer stream observation or speaker event already won. It reports whether
// the observation was accepted.
func (c *DeviceConnection) CompleteEventStreamObservation(generation uint64, connected bool, at time.Time) bool {
	accepted, _ := c.CompleteEventStreamObservationChanged(generation, connected, at)

	return accepted
}

// CompleteEventStreamObservationChanged additionally reports whether the
// accepted sample changed the browser-visible connectivity projection.
func (c *DeviceConnection) CompleteEventStreamObservationChanged(
	generation uint64,
	connected bool,
	at time.Time,
) (bool, bool) {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	if generation <= c.lastEventStreamGeneration {
		return false, false
	}

	c.lastEventStreamGeneration = generation

	c.eventStreamConnected = connected
	if connected {
		c.recordDirectSuccessLocked(at)
	}

	changed := false

	c.UpdateStatus(func(status *DeviceStatus) {
		previousConnectivity := projectedConnectivity(status)

		c.applyConnectivityLocked(status, at)
		changed = previousConnectivity != projectedConnectivity(status)
	})

	return true, changed
}

// ObserveEventStream records an immediate transport transition without an
// external sampling window.
func (c *DeviceConnection) ObserveEventStream(connected bool, at time.Time) bool {
	c.ObserveEventStreamChanged(connected, at)

	return true
}

// ObserveEventStreamChanged additionally reports whether the browser-visible
// connectivity projection changed.
func (c *DeviceConnection) ObserveEventStreamChanged(connected bool, at time.Time) bool {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	c.nextEventStreamGeneration++
	c.lastEventStreamGeneration = c.nextEventStreamGeneration

	c.eventStreamConnected = connected
	if connected {
		c.recordDirectSuccessLocked(at)
	}

	changed := false

	c.UpdateStatus(func(status *DeviceStatus) {
		previousConnectivity := projectedConnectivity(status)

		c.applyConnectivityLocked(status, at)
		changed = previousConnectivity != projectedConnectivity(status)
	})

	return changed
}

// MarkEventStreamActivity records an ordinary speaker event that is handled by
// a field-specific path such as name or group updates.
func (c *DeviceConnection) MarkEventStreamActivity(at time.Time) {
	c.MarkEventStreamActivityChanged(at)
}

// MarkEventStreamActivityChanged additionally reports whether the
// browser-visible connectivity projection changed.
func (c *DeviceConnection) MarkEventStreamActivityChanged(at time.Time) bool {
	return c.ObserveEventStreamChanged(true, at)
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
	c.nowPlayingMu.Lock()
	defer c.nowPlayingMu.Unlock()

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

	if success {
		c.httpReachable = true
		c.consecutiveFailures = 0
		c.recordDirectSuccessLocked(at)
	} else {
		c.httpReachable = false
		c.consecutiveFailures++
	}

	c.UpdateStatus(func(status *DeviceStatus) {
		if merge != nil && knownGeneration && pollEventGeneration == c.speakerEventGen {
			merge(status)
		}

		c.applyConnectivityLocked(status, at)

		if success {
			status.LastActivity = at
		}
	})

	return true
}

func (c *DeviceConnection) markEventStreamActivityLocked(at time.Time) {
	c.nextEventStreamGeneration++
	c.lastEventStreamGeneration = c.nextEventStreamGeneration
	c.eventStreamConnected = true
	c.recordDirectSuccessLocked(at)
}

func (c *DeviceConnection) recordDirectSuccessLocked(at time.Time) {
	if c.lastDirectSuccess.IsZero() || at.After(c.lastDirectSuccess) {
		c.lastDirectSuccess = at
	}
}

func (c *DeviceConnection) applyConnectivityLocked(status *DeviceStatus, at time.Time) {
	connectivity := c.connectivityLocked(at)
	status.Connectivity = connectivity
	status.HTTPReachable = c.httpReachable
	status.WebSocketConnected = c.eventStreamConnected
	status.IsConnected = connectivity != ConnectivityOffline
}

func (c *DeviceConnection) connectivityLocked(at time.Time) Connectivity {
	if c.httpReachable || c.eventStreamConnected {
		return ConnectivityOnline
	}

	if c.speakerConnectionKnown && c.speakerConnectionConnected &&
		withinConnectivityGrace(at, c.speakerConnectionObserved) {
		return ConnectivityStale
	}

	if c.consecutiveFailures < offlineFailureThreshold ||
		withinConnectivityGrace(at, c.lastDirectSuccess) {
		return ConnectivityStale
	}

	return ConnectivityOffline
}

func withinConnectivityGrace(at, success time.Time) bool {
	if success.IsZero() {
		return false
	}

	if at.Before(success) {
		return true
	}

	return at.Sub(success) < offlineGracePeriod
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
// request. Only the latest started request may later update Group. The last
// verified balance remains visible while the topology is revalidated; the
// generation fence still prevents balance writes until the refresh completes.
func (c *DeviceConnection) BeginGroupRefresh() uint64 {
	var generation uint64

	c.WithBalanceWriteFence(func() {
		c.groupMu.Lock()
		defer c.groupMu.Unlock()

		c.balanceMu.Lock()
		defer c.balanceMu.Unlock()

		c.groupGeneration++
		c.balanceGen++
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

		if changed {
			s.Balance = nil
			s.BalanceRevision++
		}

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

// BeginZoneRefresh starts a write-safety generation for an asynchronous master
// /getZone request. It invalidates older responses without hiding the retained
// browser projection during routine polling.
func (c *DeviceConnection) BeginZoneRefresh() uint64 {
	return c.beginZoneRefresh(false)
}

// BeginZoneProjectionRefresh additionally hides the retained zone until this
// topology-event readback succeeds, fails, or is rejected.
func (c *DeviceConnection) BeginZoneProjectionRefresh() uint64 {
	return c.beginZoneRefresh(true)
}

func (c *DeviceConnection) beginZoneRefresh(hideProjection bool) uint64 {
	var generation uint64

	c.zoneWriteMu.Lock()
	defer c.zoneWriteMu.Unlock()

	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()

	c.zoneGeneration++

	generation = c.zoneGeneration
	if hideProjection || c.pendingZoneProjectionGeneration != 0 {
		c.pendingZoneProjectionGeneration = generation
	}

	return generation
}

// StatusForProjection captures status and whether its cached zone is awaiting
// authoritative confirmation under one zone-generation lock. Callers can hide
// an uncertain topology without racing a newly reserved refresh.
func (c *DeviceConnection) StatusForProjection() (*DeviceStatus, bool) {
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()

	return c.Status(), c.pendingZoneProjectionGeneration != 0
}

// AbandonZoneRefresh retires one current projection refresh without accepting
// its result. The retained cache becomes visible again, while the generation
// remains unconfirmed so zone-owned writes still fail closed.
func (c *DeviceConnection) AbandonZoneRefresh(generation uint64) bool {
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()

	if generation != c.zoneGeneration ||
		c.pendingZoneProjectionGeneration != generation {
		return false
	}

	c.pendingZoneProjectionGeneration = 0

	return true
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
	accepted, _ := c.ApplyPolledZoneChanged(generation, queriedDeviceID, zone)

	return accepted
}

// ApplyPolledZoneChanged additionally reports whether the browser projection
// changed. A current but invalid response retires only the temporary projection
// barrier; it does not confirm the generation or replace the retained cache.
func (c *DeviceConnection) ApplyPolledZoneChanged(
	generation uint64,
	queriedDeviceID string,
	zone *models.ZoneInfo,
) (bool, bool) {
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()

	if generation != c.zoneGeneration {
		return false, false
	}

	projectionChanged := c.pendingZoneProjectionGeneration == generation
	c.pendingZoneProjectionGeneration = 0

	if zone == nil {
		return false, projectionChanged
	}

	master := strings.TrimSpace(zone.Master)

	queriedDeviceID = strings.TrimSpace(queriedDeviceID)
	if queriedDeviceID == "" ||
		(master == "" && len(zone.Members) != 0) ||
		(master != "" && master != queriedDeviceID) {
		return false, projectionChanged
	}

	c.confirmedZoneGeneration = generation
	zoneChanged := c.replaceZone(normalizeZone(zone))

	return true, projectionChanged || zoneChanged
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
