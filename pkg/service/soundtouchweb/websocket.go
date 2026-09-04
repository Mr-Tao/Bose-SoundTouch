// Package soundtouchweb contains WebSocket handlers for real-time communication.
package soundtouchweb

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

const defaultWebSocketWriteTimeout = 2 * time.Second

// checkWebSocketOrigin is gorilla's own same-origin default (fail the
// handshake only when an Origin header is present and doesn't match the
// request host), except the comparison ignores port -- see
// sameHostIgnoringPort for why.
func checkWebSocketOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil {
		return false
	}

	return sameHostIgnoringPort(u.Host, r.Host)
}

// sameHostIgnoringPort reports whether a and b name the same hostname,
// ignoring any port suffix.
//
// A reverse proxy commonly forwards a portless Host header regardless of
// what public port it's listening on -- nginx's $host variable never
// includes the port, unlike $http_host -- while a browser's Origin header
// for a WebSocket handshake keeps an explicit, non-default port. Comparing
// ports as well as host would reject that handshake whenever the proxy's
// public listener uses a non-default port (e.g. :8443, a realistic shape
// for multi-service hosting behind one proxy), even though the hostname
// genuinely matches. This project's documented reverse-proxy config
// (HTTPS-SETUP.md) relies on exactly that forwarding behavior.
//
// Trade-off: ignoring port also means two unrelated services sharing one
// hostname on different ports would not be distinguished by this check
// alone. Accepted here since gorilla's own default already doesn't check
// scheme either, and the alternative is silently breaking the documented,
// encouraged reverse-proxy deployment.
func sameHostIgnoringPort(a, b string) bool {
	if h, _, err := net.SplitHostPort(a); err == nil {
		a = h
	}

	if h, _, err := net.SplitHostPort(b); err == nil {
		b = h
	}

	return strings.EqualFold(a, b)
}

type webSocketWriter interface {
	SetWriteDeadline(time.Time) error
	WriteJSON(interface{}) error
	WriteMessage(int, []byte) error
}

// webSocketWriteBatch gives every write a fresh deadline. A stalled client is
// bounded without passing an already-expired deadline to later healthy clients.
type webSocketWriteBatch struct {
	timeout time.Duration
}

func (batch webSocketWriteBatch) writeJSON(conn webSocketWriter, value interface{}) error {
	if err := conn.SetWriteDeadline(time.Now().Add(batch.timeout)); err != nil {
		return err
	}

	return conn.WriteJSON(value)
}

func (batch webSocketWriteBatch) writeMessage(conn webSocketWriter, messageType int, data []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(batch.timeout)); err != nil {
		return err
	}

	return conn.WriteMessage(messageType, data)
}

// writeTimeout returns the configured browser WebSocket write timeout, or
// defaultWebSocketWriteTimeout when unset.
func (app *WebApp) writeTimeout() time.Duration {
	if app.webSocketWriteTimeout > 0 {
		return app.webSocketWriteTimeout
	}

	return defaultWebSocketWriteTimeout
}

// errConnUnregistered is returned by withConnWrite when conn is not (or is
// no longer) present in WSClients -- e.g. it was concurrently removed.
var errConnUnregistered = errors.New("websocket connection is not registered")

// withConnWrite serializes writes to a single browser WebSocket connection.
// Gorilla permits only one concurrent writer per connection (vendored
// gorilla/websocket@v1.5.3 doc.go); this is the per-connection replacement
// for the removed global write mutex, so a stalled client's writes never
// block writes to any OTHER connection or unrelated HTTP handlers.
func (app *WebApp) withConnWrite(conn *websocket.Conn, write func(webSocketWriteBatch) error) error {
	app.WSMutex.RLock()
	mu := app.WSClients[conn]
	app.WSMutex.RUnlock()

	if mu == nil {
		return errConnUnregistered
	}

	mu.Lock()
	defer mu.Unlock()

	return write(webSocketWriteBatch{timeout: app.writeTimeout()})
}

// withDiscoveryStatusWrite keeps the authoritative discovery state ordered
// with the frame that publishes it to browser clients, across concurrent
// publications. It is deliberately independent of connection registration
// -- see discoveryPublishMu's doc comment on WebApp.
func (app *WebApp) withDiscoveryStatusWrite(
	status *webtypes.DiscoveryStatus,
	write func(webSocketWriteBatch, []*websocket.Conn) error,
) error {
	app.discoveryPublishMu.Lock()
	defer app.discoveryPublishMu.Unlock()

	app.discoveryStatus.Store(status)

	return write(webSocketWriteBatch{timeout: app.writeTimeout()}, app.globalWebSocketClients())
}

func (app *WebApp) globalWebSocketClients() []*websocket.Conn {
	app.WSMutex.RLock()
	defer app.WSMutex.RUnlock()

	clients := make([]*websocket.Conn, 0, len(app.WSClients))
	for client := range app.WSClients {
		clients = append(clients, client)
	}

	return clients
}

// removeGlobalWebSocketClient unregisters client and closes it, holding its
// own write lock across Close() so this never races an in-flight or
// about-to-start write to the same connection.
func (app *WebApp) removeGlobalWebSocketClient(client *websocket.Conn) {
	app.WSMutex.Lock()

	mu, ok := app.WSClients[client]
	if ok {
		delete(app.WSClients, client)
	}
	app.WSMutex.Unlock()

	if !ok {
		return
	}

	mu.Lock()
	_ = client.Close()
	mu.Unlock()
}

// registerGlobalWebSocket creates conn's write lock already held, inserts it
// into WSClients, then sends its initial frames -- so a broadcast racing
// right after insertion blocks on this connection's own lock until the
// initial snapshot is complete, with no shared lock required.
func (app *WebApp) registerGlobalWebSocket(conn *websocket.Conn) error {
	connMu := &sync.Mutex{}
	connMu.Lock()
	defer connMu.Unlock()

	app.WSMutex.Lock()
	app.WSClients[conn] = connMu
	app.WSMutex.Unlock()

	batch := webSocketWriteBatch{timeout: app.writeTimeout()}

	if ds, ok := app.discoveryStatus.Load().(*webtypes.DiscoveryStatus); ok {
		if err := batch.writeJSON(conn, webtypes.WebSocketMessage{
			Type: "discovery_status",
			Data: ds,
		}); err != nil {
			return err
		}
	}

	return batch.writeJSON(conn, webtypes.WebSocketMessage{
		Type: "devices",
		Data: app.deviceViewSnapshot(),
	})
}

// queueBroadcastIfChanged schedules a device-list broadcast when an
// apply-event/apply-poll call changed something dashboard-visible. Every
// such call site (including Group's own, separately-ordered path) routes
// through this so a future change to the shared policy has one place to
// change.
func (app *WebApp) queueBroadcastIfChanged(changed bool) bool {
	if changed {
		app.QueueDeviceListBroadcast()
	}

	return changed
}

// applySpeakerStatusEvent stores one speaker event for field and immediately
// publishes a fresh device projection when its dashboard-visible payload
// changed. Ordering against a concurrent poll of the same field is handled
// by ApplyFieldEvent; a different field's poll or event is never affected.
func (app *WebApp) applySpeakerStatusEvent(
	conn *webtypes.DeviceConnection,
	field webtypes.StatusField,
	mut func(*webtypes.DeviceStatus) bool,
) bool {
	changed := false

	conn.ApplyFieldEvent(field, func(status *webtypes.DeviceStatus) {
		changed = mut(status)
	})

	return app.queueBroadcastIfChanged(changed)
}

func (app *WebApp) applyNowPlayingEvent(
	conn *webtypes.DeviceConnection,
	nowPlaying *models.NowPlaying,
) bool {
	return app.applySpeakerStatusEvent(conn, webtypes.FieldNowPlaying, func(status *webtypes.DeviceStatus) bool {
		changed := !reflect.DeepEqual(status.NowPlaying, nowPlaying)
		status.NowPlaying = nowPlaying
		status.LastActivity = time.Now()

		return changed
	})
}

func (app *WebApp) applyVolumeEvent(
	conn *webtypes.DeviceConnection,
	volume *models.Volume,
) bool {
	return app.applySpeakerStatusEvent(conn, webtypes.FieldVolume, func(status *webtypes.DeviceStatus) bool {
		changed := !reflect.DeepEqual(status.Volume, volume)
		status.Volume = volume
		status.LastActivity = time.Now()

		return changed
	})
}

func (app *WebApp) applyConnectionStateEvent(
	conn *webtypes.DeviceConnection,
	connected bool,
) bool {
	return app.applySpeakerStatusEvent(conn, webtypes.FieldConnectivity, func(status *webtypes.DeviceStatus) bool {
		changed := status.IsConnected != connected
		status.IsConnected = connected
		status.LastActivity = time.Now()

		return changed
	})
}

func (app *WebApp) applyPresetEvent(
	conn *webtypes.DeviceConnection,
	presets *models.Presets,
) bool {
	return app.applySpeakerStatusEvent(conn, webtypes.FieldPresets, func(status *webtypes.DeviceStatus) bool {
		changed := !reflect.DeepEqual(status.Presets, presets)
		status.Presets = presets
		status.LastActivity = time.Now()

		return changed
	})
}

func (app *WebApp) applyBassEvent(
	conn *webtypes.DeviceConnection,
	bass *models.Bass,
) bool {
	return app.applySpeakerStatusEvent(conn, webtypes.FieldBass, func(status *webtypes.DeviceStatus) bool {
		changed := !reflect.DeepEqual(status.Bass, bass)
		status.Bass = bass
		status.LastActivity = time.Now()

		return changed
	})
}

// HandleWebSocket handles WebSocket connections for real-time updates
func (app *WebApp) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := app.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	defer func() {
		app.removeGlobalWebSocketClient(conn)
	}()

	// Register and send initial frames under the same write lock used by
	// broadcasts and periodic updates. No other goroutine can write this
	// connection before its initial snapshot is complete.
	if err := app.registerGlobalWebSocket(conn); err != nil {
		log.Printf("Failed to send initial data: %v", err)
		return
	}

	// Keep connection alive and send updates
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Set up ping handler to detect client disconnects
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Set initial read deadline
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	// Handle incoming messages in a separate goroutine
	go func() {
		defer conn.Close()

		for {
			if _, _, err := conn.NextReader(); err != nil {
				log.Printf("WebSocket read error: %v", err)
				return
			}
		}
	}()

	// Main loop for sending periodic updates
	for range ticker.C {
		if err := app.withConnWrite(conn, func(batch webSocketWriteBatch) error {
			if err := batch.writeMessage(conn, websocket.PingMessage, []byte{}); err != nil {
				return err
			}

			// Capture after taking this connection's writer lock so a newer
			// broadcast to this same connection cannot be followed by a
			// periodic frame captured from older state.
			for _, message := range app.periodicPlayerMessages() {
				if err := batch.writeJSON(conn, message); err != nil {
					return err
				}
			}

			return nil
		}); err != nil {
			log.Printf("Failed to send periodic WebSocket update: %v", err)
			return
		}
	}
}

// periodicPlayerMessages refreshes the projected inventory while retaining
// the established per-device status_update stream for API clients.
func (app *WebApp) periodicPlayerMessages() []webtypes.WebSocketMessage {
	snapshot := captureDeviceProjectionEntries(app.DeviceSnapshot())
	messages := []webtypes.WebSocketMessage{{
		Type: "devices",
		Data: projectCapturedDeviceEntries(snapshot),
	}}

	for _, entry := range snapshot {
		if entry.Status == nil || !entry.Status.IsConnected {
			continue
		}

		messages = append(messages, webtypes.WebSocketMessage{
			Type:     "status_update",
			DeviceID: entry.ID,
			Data:     entry.Status,
		})
	}

	return messages
}

// HandleAPIDiscover triggers device discovery
func (app *WebApp) HandleAPIDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		app.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	response := webtypes.APIResponse{
		Success: true,
		Data:    map[string]string{"message": "Discovery started"},
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// ConnectDeviceWebSocket establishes a WebSocket connection to a device
// and keeps it alive: on disconnect or connect failure, it reconnects
// with exponential backoff (1 s → 30 s cap, reset after each successful
// connect). The goroutine runs for the lifetime of the device entry,
// so status flows from the speaker keep streaming through transient
// network blips, speaker reboots, and idle timeouts.
//
// conn.WebSocket is only updated on a successful (re)connect, never
// cleared, so the duplicate-spawn guards at the callsites (which check
// `if device.WebSocket == nil`) stay correct — once this goroutine is
// running for a device, no second one is needed.
func (app *WebApp) ConnectDeviceWebSocket(deviceID string, conn *webtypes.DeviceConnection) {
	// Skip WebSocket connection if client is not available (e.g., in tests)
	if conn.Client == nil {
		return
	}

	const (
		initialBackoff = 1 * time.Second
		maxBackoff     = 30 * time.Second
	)

	backoff := initialBackoff

	// Tracks the last now_playing source seen so a speaker stuck reporting an
	// error source is logged once per transition into it, not on every event.
	var prevSource string

	for {
		// Stop if the device was removed from the registry (conn.Close()).
		select {
		case <-conn.Done():
			return
		default:
		}

		wsClient := conn.Client.NewWebSocketClient(nil)

		// Setup event handlers. Each handler funnels its change through
		// UpdateStatus so concurrent events and the periodic poller
		// (UpdateDeviceStatus) cannot lose each other's writes.
		wsClient.OnNowPlaying(func(event *models.NowPlayingUpdatedEvent) {
			np := &event.NowPlaying

			// A /select returns 200 even when the source is rejected; the
			// failure shows up here as a transition to an error source. Log it
			// so it lands in a diagnostic export without needing a live trace.
			if np.Source != prevSource && isErrorSource(np.Source) {
				logNowPlayingError(deviceID, np.Source, np.SourceAccount)
			}

			prevSource = np.Source

			app.applyNowPlayingEvent(conn, np)
		})

		wsClient.OnVolumeUpdated(func(event *models.VolumeUpdatedEvent) {
			app.applyVolumeEvent(conn, &event.Volume)
		})

		wsClient.OnConnectionState(func(event *models.ConnectionStateUpdatedEvent) {
			app.applyConnectionStateEvent(conn, event.ConnectionState.IsConnected())
		})

		wsClient.OnPresetUpdated(func(event *models.PresetUpdatedEvent) {
			app.applyPresetEvent(conn, &event.Presets)
		})

		wsClient.OnBassUpdated(func(event *models.BassUpdatedEvent) {
			app.applyBassEvent(conn, &event.Bass)
		})

		wsClient.OnGroupUpdated(func(event *models.GroupUpdatedEvent) {
			app.applyGroupUpdatedEvent(conn, event)
		})

		if err := wsClient.Connect(); err != nil {
			log.Printf("Failed to connect WebSocket for device %s: %v (retrying in %s)", sanitizeLog(deviceID), err, backoff)

			if sleepOrDone(conn, backoff) {
				return
			}

			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}

			continue
		}

		conn.WebSocket = wsClient

		app.applyConnectionStateEvent(conn, true)

		log.Printf("WebSocket connected for device %s", sanitizeLog(deviceID))

		// Fetch current state immediately: speakers do not replay events on
		// new WebSocket connections, so anything that changed while we were
		// disconnected would otherwise stay stale until the next WS event.
		go app.UpdateDeviceStatus(deviceID, conn)

		// Reset backoff after a successful connect so the next failure
		// starts at the lowest cadence again.
		backoff = initialBackoff

		// Block until the device-side WebSocket disconnects.
		wsClient.Wait()

		app.applyConnectionStateEvent(conn, false)

		log.Printf("WebSocket disconnected for device %s — reconnecting in %s", sanitizeLog(deviceID), backoff)

		if sleepOrDone(conn, backoff) {
			return
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// sleepOrDone waits for d to elapse or for the connection to be closed,
// whichever comes first. It returns true if the connection was closed
// (the caller should stop), false if the timer fired normally.
func sleepOrDone(conn *webtypes.DeviceConnection, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return false
	case <-conn.Done():
		return true
	}
}

// UpdateDeviceStatus fetches current status from the device.
//
// Network calls run outside any atomic merge so slow I/O never blocks a
// concurrent CAS retry. Each field (NowPlaying/Volume/Presets/Sources/Bass,
// plus derived connectivity) is merged and ordered independently via its own
// StatusField generation (BeginFieldPoll/CompleteFieldPoll/ApplyFieldEvent in
// webtypes) -- a real-time push event, or a concurrent poll, for one field
// can supersede only that field. A slow-but-successful fetch for one field
// is never discarded merely because a DIFFERENT field's event or poll
// completion happened to land first.
func (app *WebApp) UpdateDeviceStatus(_ string, conn *webtypes.DeviceConnection) {
	// Skip status update if client is not available (e.g., in tests)
	if conn.Client == nil {
		return
	}

	nowPlayingGen := conn.BeginFieldPoll(webtypes.FieldNowPlaying)
	volumeGen := conn.BeginFieldPoll(webtypes.FieldVolume)
	presetsGen := conn.BeginFieldPoll(webtypes.FieldPresets)
	sourcesGen := conn.BeginFieldPoll(webtypes.FieldSources)
	bassGen := conn.BeginFieldPoll(webtypes.FieldBass)
	connectivityGen := conn.BeginFieldPoll(webtypes.FieldConnectivity)

	// /getGroup must be gated to ST10 models -- see Client.GetGroup's doc
	// comment (verified against real hardware: a ST20 never replies at all,
	// hanging until the client's timeout instead of returning quickly).
	stereoCapable := stereoPairCapable(conn.DeviceInfo)

	var groupGeneration uint64
	if stereoCapable {
		groupGeneration = conn.BeginGroupRefresh()
	}

	// Phase 1: slow network fetches. Local vars only, no shared state
	// is touched yet. Errors are recorded so the merge below can tell
	// "field N stayed unchanged" apart from "field N got refreshed".
	nowPlaying, nowPlayingErr := conn.Client.GetNowPlaying()
	volume, volumeErr := conn.Client.GetVolume()
	presets, presetsErr := conn.Client.GetPresets()
	sources, sourcesErr := conn.Client.GetSources()
	bass, bassErr := conn.Client.GetBass()

	var (
		group    *models.Group
		groupErr error
	)

	if stereoCapable {
		group, groupErr = conn.Client.GetGroup()
	}

	// Phase 2: fast, independently-ordered merges. Each field applies only
	// if this round's fetch succeeded AND no newer poll or push event has
	// already applied for that specific field.
	anyFetchSucceeded := false

	if nowPlayingErr == nil {
		anyFetchSucceeded = true

		conn.CompleteFieldPoll(webtypes.FieldNowPlaying, nowPlayingGen, func(s *webtypes.DeviceStatus) {
			s.NowPlaying = nowPlaying
			s.LastActivity = time.Now()
		})
	}

	if volumeErr == nil {
		anyFetchSucceeded = true

		conn.CompleteFieldPoll(webtypes.FieldVolume, volumeGen, func(s *webtypes.DeviceStatus) {
			s.Volume = volume
			s.LastActivity = time.Now()
		})
	}

	if presetsErr == nil {
		anyFetchSucceeded = true

		conn.CompleteFieldPoll(webtypes.FieldPresets, presetsGen, func(s *webtypes.DeviceStatus) {
			s.Presets = presets
			s.LastActivity = time.Now()
		})
	}

	if sourcesErr == nil {
		anyFetchSucceeded = true

		conn.CompleteFieldPoll(webtypes.FieldSources, sourcesGen, func(s *webtypes.DeviceStatus) {
			s.Sources = sources
			s.LastActivity = time.Now()
		})
	}

	if bassErr == nil {
		anyFetchSucceeded = true

		conn.CompleteFieldPoll(webtypes.FieldBass, bassGen, func(s *webtypes.DeviceStatus) {
			s.Bass = bass
			s.LastActivity = time.Now()
		})
	}

	// Mark as connected if we successfully got at least one status from
	// this round. Mirrors prior behaviour: deliberately does NOT fold
	// groupErr in here. GetGroup is gated to stereo-capable models and
	// trivially succeeds even when a device is otherwise struggling (an
	// empty <group/> is a near-guaranteed reply), so counting it would let
	// a device report connected while every substantive status fetch above
	// actually failed this round.
	conn.CompleteFieldPoll(webtypes.FieldConnectivity, connectivityGen, func(s *webtypes.DeviceStatus) {
		s.IsConnected = anyFetchSucceeded
		s.LastActivity = time.Now()
	})

	if stereoCapable && groupErr == nil {
		conn.ApplyPolledGroup(groupGeneration, group)
	}
}

func (app *WebApp) applyGroupUpdatedEvent(
	conn *webtypes.DeviceConnection,
	event *models.GroupUpdatedEvent,
) bool {
	return app.queueBroadcastIfChanged(conn.ApplyGroupEvent(&event.Group, time.Now()))
}

// HandleDeviceWebSocket handles individual device WebSocket connections for real-time device-specific updates
func (app *WebApp) HandleDeviceWebSocket(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if deviceID == "" {
		http.Error(w, "Device ID required", http.StatusBadRequest)
		return
	}

	device, exists := app.GetDevice(deviceID)
	if !exists {
		http.Error(w, "Device not found", http.StatusNotFound)
		return
	}

	conn, err := app.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Device WebSocket upgrade failed for %s: %v", sanitizeLog(deviceID), err)
		return
	}
	defer conn.Close()

	log.Printf("Device WebSocket connected for %s", sanitizeLog(deviceID))

	// Send initial device status
	initialMessage := webtypes.WebSocketMessage{
		Type:     "device_status",
		DeviceID: deviceID,
		Data: map[string]interface{}{
			"info":   device.DeviceInfo,
			"status": device.Status(),
		},
	}

	if err := conn.WriteJSON(initialMessage); err != nil {
		log.Printf("Failed to send initial device status: %v", err)
		return
	}

	// Set up ping handler to detect client disconnects
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Set initial read deadline
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	// Handle incoming messages in a separate goroutine
	go func() {
		defer conn.Close()

		for {
			if _, _, err := conn.NextReader(); err != nil {
				log.Printf("Device WebSocket read error for %s: %v", sanitizeLog(deviceID), err)
				return
			}
		}
	}()

	// Send periodic device status updates
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Send ping to check if client is still connected
		if err := conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
			log.Printf("Failed to send ping to device WebSocket %s: %v", sanitizeLog(deviceID), err)
			return
		}

		// Send device status update
		status := device.Status()
		statusMessage := webtypes.WebSocketMessage{
			Type:     "device_status",
			DeviceID: deviceID,
			Data: map[string]interface{}{
				"info":   device.DeviceInfo,
				"status": status,
			},
		}

		if err := conn.WriteJSON(statusMessage); err != nil {
			log.Printf("Failed to send device status update for %s: %v", sanitizeLog(deviceID), err)
			return
		}

		// If device has active WebSocket connection to SoundTouch device,
		// also send any real-time updates from that connection
		if device.WebSocket != nil && status.IsConnected {
			realtimeMessage := webtypes.WebSocketMessage{
				Type:     "device_realtime",
				DeviceID: deviceID,
				Data: map[string]interface{}{
					"nowPlaying": status.NowPlaying,
					"volume":     status.Volume,
					"timestamp":  time.Now(),
				},
			}

			if err := conn.WriteJSON(realtimeMessage); err != nil {
				log.Printf("Failed to send realtime update for %s: %v", sanitizeLog(deviceID), err)
				return
			}
		}
	}
}
