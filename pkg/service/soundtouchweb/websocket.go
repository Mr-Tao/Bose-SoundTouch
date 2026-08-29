// Package soundtouchweb contains WebSocket handlers for real-time communication.
package soundtouchweb

import (
	"encoding/json"
	"log"
	"net/http"
	"reflect"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

const defaultWebSocketWriteTimeout = 2 * time.Second

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

// withGlobalWebSocketWrite is the single write seam for application-wide
// browser WebSockets. The client registry has a separate lock so registration
// and cleanup never hold a mutex across network I/O.
func (app *WebApp) withGlobalWebSocketWrite(write func(webSocketWriteBatch) error) error {
	app.webSocketWriteMu.Lock()
	defer app.webSocketWriteMu.Unlock()

	timeout := app.webSocketWriteTimeout
	if timeout <= 0 {
		timeout = defaultWebSocketWriteTimeout
	}

	return write(webSocketWriteBatch{timeout: timeout})
}

// withDiscoveryStatusWrite keeps the authoritative discovery state ordered
// with the frame that publishes it to browser clients.
func (app *WebApp) withDiscoveryStatusWrite(
	status *webtypes.DiscoveryStatus,
	write func(webSocketWriteBatch, []*websocket.Conn) error,
) error {
	return app.withGlobalWebSocketWrite(func(batch webSocketWriteBatch) error {
		app.discoveryStatus.Store(status)

		return write(batch, app.globalWebSocketClients())
	})
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

func (app *WebApp) removeGlobalWebSocketClient(client *websocket.Conn) {
	app.WSMutex.Lock()
	delete(app.WSClients, client)
	app.WSMutex.Unlock()

	_ = client.Close()
}

func (app *WebApp) registerGlobalWebSocket(conn *websocket.Conn) error {
	return app.withGlobalWebSocketWrite(func(batch webSocketWriteBatch) error {
		app.WSMutex.Lock()
		app.WSClients[conn] = true
		app.WSMutex.Unlock()

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
	})
}

// applySpeakerStatusEvent stores one speaker event and immediately publishes
// a fresh device projection when its dashboard-visible payload changed.
func (app *WebApp) applySpeakerStatusEvent(
	conn *webtypes.DeviceConnection,
	mut func(*webtypes.DeviceStatus) bool,
) bool {
	changed := false

	conn.ApplySpeakerEvent(func(status *webtypes.DeviceStatus) {
		changed = mut(status)
	})

	if changed {
		app.QueueDeviceListBroadcast()
	}

	return changed
}

func (app *WebApp) applyNowPlayingEvent(
	conn *webtypes.DeviceConnection,
	nowPlaying *models.NowPlaying,
) bool {
	return app.applySpeakerStatusEvent(conn, func(status *webtypes.DeviceStatus) bool {
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
	return app.applySpeakerStatusEvent(conn, func(status *webtypes.DeviceStatus) bool {
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
	return app.applySpeakerStatusEvent(conn, func(status *webtypes.DeviceStatus) bool {
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
	return app.applySpeakerStatusEvent(conn, func(status *webtypes.DeviceStatus) bool {
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
	return app.applySpeakerStatusEvent(conn, func(status *webtypes.DeviceStatus) bool {
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
		if err := app.withGlobalWebSocketWrite(func(batch webSocketWriteBatch) error {
			if err := batch.writeMessage(conn, websocket.PingMessage, []byte{}); err != nil {
				return err
			}

			// Capture after taking the writer lock so a newer broadcast cannot be
			// followed by a periodic frame captured from older state.
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
// Network calls run outside the atomic merge so the CAS loop in
// UpdateStatus stays fast and doesn't retry slow IO. WebSocket event
// handlers running concurrently are not lost: their UpdateStatus
// runs against whichever snapshot they observe, and the merge below
// sees their changes when it CAS-loops onto the latest status.
func (app *WebApp) UpdateDeviceStatus(_ string, conn *webtypes.DeviceConnection) {
	// Skip status update if client is not available (e.g., in tests)
	if conn.Client == nil {
		return
	}

	pollGeneration := conn.BeginStatusPoll()

	// /getGroup is ST10-only; ST20/ST30 may accept the request but never reply.
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

	// Phase 2: fast merge. Only fields we successfully fetched
	// overwrite; everything else keeps the value other goroutines may
	// have just written.
	conn.CompleteStatusPoll(pollGeneration, func(s *webtypes.DeviceStatus) {
		statusUpdated := false

		if nowPlayingErr == nil {
			s.NowPlaying = nowPlaying
			statusUpdated = true
		}

		if volumeErr == nil {
			s.Volume = volume
			statusUpdated = true
		}

		if presetsErr == nil {
			s.Presets = presets
			statusUpdated = true
		}

		if sourcesErr == nil {
			s.Sources = sources
			statusUpdated = true
		}

		if bassErr == nil {
			s.Bass = bass
			statusUpdated = true
		}

		statusUpdated = statusUpdated || (stereoCapable && groupErr == nil)

		// Mark as connected if we successfully got at least one
		// status from this round. Mirrors prior behaviour.
		s.IsConnected = statusUpdated
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
	changed := conn.ApplyGroupEvent(&event.Group, time.Now())
	if changed {
		app.QueueDeviceListBroadcast()
	}

	return changed
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
