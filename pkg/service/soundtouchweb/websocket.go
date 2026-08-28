// Package soundtouchweb contains WebSocket handlers for real-time communication.
package soundtouchweb

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
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
// bounded without passing an already-expired deadline to healthy clients later
// in the same broadcast.
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

// withGlobalWebSocketWrite is the single write seam for the application-wide
// and per-device browser WebSocket connections. Gorilla permits one concurrent
// writer per connection. The client registry has a separate lock so connection
// registration and cleanup never wait on network I/O.
func (app *WebApp) withGlobalWebSocketWrite(write func(webSocketWriteBatch) error) error {
	app.webSocketWriteMu.Lock()
	defer app.webSocketWriteMu.Unlock()

	timeout := app.webSocketWriteTimeout
	if timeout <= 0 {
		timeout = defaultWebSocketWriteTimeout
	}

	return write(webSocketWriteBatch{timeout: timeout})
}

// awaitPriorGlobalWebSocketWrites is an ordering barrier. Once it returns, any
// writer that captured state before the caller's projection has completed; a
// later writer must capture the newly projected state under the same lock.
func (app *WebApp) awaitPriorGlobalWebSocketWrites() {
	_ = app.withGlobalWebSocketWrite(func(webSocketWriteBatch) error { return nil })
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

	// Send initial frames under the same write lock used by broadcasts and
	// periodic updates so no Gorilla writes can overlap on this connection.
	if err := app.withGlobalWebSocketWrite(func(batch webSocketWriteBatch) error {
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
	}); err != nil {
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
			// Capture after taking the writer lock. A lifecycle broadcast that
			// already completed cannot be followed by an older periodic snapshot.
			messages := app.periodicPlayerMessages()

			if err := batch.writeMessage(conn, websocket.PingMessage, []byte{}); err != nil {
				return err
			}

			for _, message := range messages {
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
		if entry.Status == nil {
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

// ConnectDeviceWebSocket starts the single event-transport supervisor for a
// device. Initial connection failures are retried here; after the first
// success WebSocketClient owns transport reconnects and this supervisor only
// observes their state until the device is removed.
func (app *WebApp) ConnectDeviceWebSocket(deviceID string, conn *webtypes.DeviceConnection) {
	// Skip WebSocket connection if client is not available (e.g., in tests)
	if conn.Client == nil {
		return
	}

	if !conn.TryStartWebSocketLoop() {
		return
	}

	defer func() {
		conn.SetWebSocket(nil)
		conn.ObserveEventStream(false, time.Now())
		conn.FinishWebSocketLoop()
	}()

	const (
		initialBackoff        = 1 * time.Second
		maxBackoff            = 30 * time.Second
		transportPollInterval = 1 * time.Second
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
			activity := time.Now()
			np := &event.NowPlaying

			// A /select returns 200 even when the source is rejected; the
			// failure shows up here as a transition to an error source. Log it
			// so it lands in a diagnostic export without needing a live trace.
			if np.Source != prevSource && isErrorSource(np.Source) {
				logNowPlayingError(deviceID, np.Source, np.SourceAccount)
			}

			prevSource = np.Source

			conn.ApplySpeakerEventAt(activity, func(s *webtypes.DeviceStatus) {
				s.NowPlaying = np
				s.LastActivity = activity
			})
		})

		wsClient.OnVolumeUpdated(func(event *models.VolumeUpdatedEvent) {
			conn.ApplyVolumeEvent(&event.Volume, time.Now())
		})

		wsClient.OnConnectionState(func(event *models.ConnectionStateUpdatedEvent) {
			if !speakerConnectionEventMatches(conn, event.DeviceID) {
				log.Printf("Ignoring connection state for mismatched device %s on %s",
					sanitizeLog(event.DeviceID), sanitizeLog(deviceID))

				return
			}

			conn.ApplySpeakerConnectionEvent(webtypes.SpeakerConnectionState{
				State:  event.ConnectionState.State,
				Signal: event.ConnectionState.Signal,
			}, time.Now())
		})

		wsClient.OnPresetUpdated(func(event *models.PresetUpdatedEvent) {
			conn.ApplySpeakerEvent(func(s *webtypes.DeviceStatus) {
				s.Presets = &event.Presets
				s.LastActivity = time.Now()
			})
		})

		wsClient.OnBassUpdated(func(event *models.BassUpdatedEvent) {
			if !speakerConnectionEventMatches(conn, event.DeviceID) {
				return
			}

			conn.WithBassOperation(func() {
				conn.ApplyBassEvent(&event.Bass, time.Now())
			})
		})

		wsClient.OnGroupUpdated(func(event *models.GroupUpdatedEvent) {
			applyGroupUpdatedEvent(conn, event)
		})

		wsClient.OnBalanceUpdated(func(event *models.BalanceUpdatedEvent) {
			app.applyBalanceUpdatedEvent(deviceID, conn, event)
		})

		wsClient.OnZoneUpdated(func(event *models.ZoneUpdatedEvent) {
			conn.MarkEventStreamActivity(time.Now())

			go app.refreshZonesAfterEvent(event.DeviceID, event.Zone.Master)
		})

		wsClient.OnNameUpdated(func(event *models.NameUpdatedEvent) {
			conn.MarkEventStreamActivity(time.Now())
			conn.ApplyNameEvent(event.Name.Value)
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

		conn.SetWebSocket(wsClient)
		conn.ObserveEventStream(true, time.Now())

		log.Printf("WebSocket connected for device %s", sanitizeLog(deviceID))

		// Fetch current state immediately: speakers do not replay events on
		// new WebSocket connections, so anything that changed while we were
		// disconnected would otherwise stay stale until the next WS event.
		go app.UpdateDeviceStatus(deviceID, conn)

		transportTicker := time.NewTicker(transportPollInterval)
		defer transportTicker.Stop()

		transportConnected := true

		for {
			select {
			case <-conn.Done():
				_ = wsClient.Close()

				return
			case <-transportTicker.C:
				observation := conn.BeginEventStreamObservation()

				connected := wsClient.IsConnected()
				if !conn.CompleteEventStreamObservation(observation, connected, time.Now()) {
					continue
				}

				if connected == transportConnected {
					continue
				}

				transportConnected = connected

				if connected {
					log.Printf("WebSocket reconnected for device %s", sanitizeLog(deviceID))
					go app.UpdateDeviceStatus(deviceID, conn)
				} else {
					log.Printf("WebSocket transport disconnected for device %s", sanitizeLog(deviceID))
				}
			}
		}
	}
}

func speakerConnectionEventMatches(conn *webtypes.DeviceConnection, eventDeviceID string) bool {
	eventDeviceID = strings.TrimSpace(eventDeviceID)
	if eventDeviceID == "" {
		return true
	}

	info := conn.Info()
	if info == nil || strings.TrimSpace(info.DeviceID) == "" {
		return false
	}

	return strings.EqualFold(eventDeviceID, strings.TrimSpace(info.DeviceID))
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
// Network calls run outside the atomic merge so the CAS loop in UpdateStatus
// stays fast and never retries slow IO. A speaker event that arrives after the
// poll starts prevents the older payload from merging, while the poll may still
// update connectivity health.
func (app *WebApp) UpdateDeviceStatus(deviceID string, conn *webtypes.DeviceConnection) {
	// Skip status update if client is not available (e.g., in tests)
	if conn.Client == nil {
		return
	}

	pollGeneration := conn.BeginHTTPPoll()

	// /getGroup and /balance are ST10-only; ST20/ST30 may accept these requests
	// but never reply. Balance is narrower still: this same poll must first
	// confirm the physical device as the stereo-pair group master.
	stereoCapable := stereoPairCapable(conn.DeviceInfo)

	nameGeneration := conn.BeginNameRefresh()
	zoneGeneration := conn.BeginZoneRefresh()

	poll := fetchDeviceStatus(conn)
	stereo := app.fetchStereoStatus(deviceID, conn, stereoCapable)

	// Phase 2: fast merge. Only fields we successfully fetched
	// overwrite; everything else keeps the value other goroutines may
	// have just written.
	if poll.volumeErr == nil {
		conn.ApplyPolledVolume(poll.volumeGeneration, poll.volume)
	}

	if poll.bassErr == nil {
		conn.ApplyPolledBass(poll.bassGeneration, poll.bass)
	}

	conn.CompleteHTTPPoll(
		pollGeneration,
		deviceStatusPollSucceeded(poll, stereo, stereoCapable),
		time.Now(),
		poll.merge,
	)

	if poll.nameErr == nil {
		conn.ApplyPolledName(nameGeneration, poll.name.Value)
	}

	if poll.zoneErr == nil {
		if info := conn.Info(); info != nil {
			conn.ApplyPolledZone(zoneGeneration, info.DeviceID, poll.zone)
		}
	}
}

type deviceStatusPoll struct {
	nowPlaying              *models.NowPlaying
	name                    *models.Name
	volume                  *models.Volume
	presets                 *models.Presets
	sources                 *models.Sources
	bass                    *models.Bass
	zone                    *models.ZoneInfo
	volumeGeneration        uint64
	bassGeneration          uint64
	nowPlayingErr           error
	nameErr                 error
	volumeErr               error
	presetsErr              error
	sourcesErr              error
	bassErr                 error
	zoneErr                 error
	bassCapabilitiesOutcome webtypes.BassCapabilitiesFetchOutcome
}

// fetchDeviceStatus performs the slow non-stereo reads without touching the
// shared status snapshot. The caller merges successful fields afterwards.
func fetchDeviceStatus(conn *webtypes.DeviceConnection) deviceStatusPoll {
	var poll deviceStatusPoll

	poll.nowPlaying, poll.nowPlayingErr = conn.Client.GetNowPlaying()
	poll.name, poll.nameErr = conn.Client.GetName()
	poll.volumeGeneration = conn.BeginVolumeRefresh()
	poll.volume, poll.volumeErr = conn.Client.GetVolume()
	poll.presets, poll.presetsErr = conn.Client.GetPresets()
	poll.sources, poll.sourcesErr = conn.Client.GetSources()
	conn.WithBassOperation(func() {
		poll.bassGeneration = conn.BeginBassRefresh()
		poll.bass, poll.bassErr = conn.Client.GetBass()
	})
	poll.bassCapabilitiesOutcome, _ = conn.EnsureBassCapabilities(conn.Client.GetBassCapabilities)
	poll.zone, poll.zoneErr = conn.Client.GetZone()

	return poll
}

func (poll deviceStatusPoll) merge(status *webtypes.DeviceStatus) {
	if poll.nowPlayingErr == nil {
		status.NowPlaying = poll.nowPlaying
	}

	if poll.presetsErr == nil {
		status.Presets = poll.presets
	}

	if poll.sourcesErr == nil {
		status.Sources = poll.sources
	}
}

type stereoStatusPoll struct {
	groupErr       error
	balance        *models.Balance
	balanceErr     error
	balanceFetched bool
}

func (app *WebApp) fetchStereoStatus(
	deviceID string,
	conn *webtypes.DeviceConnection,
	stereoCapable bool,
) stereoStatusPoll {
	var poll stereoStatusPoll
	if !stereoCapable {
		return poll
	}

	conn.WithBalanceOperation(func() {
		groupGeneration := conn.BeginGroupRefresh()
		group, err := conn.Client.GetGroup()

		poll.groupErr = err
		if err != nil {
			return
		}

		if !conn.ApplyPolledGroup(groupGeneration, group) ||
			!confirmedStereoBalanceMaster(conn.Info().DeviceID, group) {
			return
		}

		refresh, ok := conn.BeginBalanceRefresh()
		if !ok || !sameGroupClaim(refresh.Group, group) ||
			!app.balanceRefreshCurrent(deviceID, conn, refresh) {
			return
		}

		poll.balanceFetched = true

		poll.balance, poll.balanceErr = conn.Client.GetBalance()
		if poll.balanceErr == nil && poll.balance != nil {
			app.applyBalanceReadback(deviceID, conn, refresh, poll.balance)
		}
	})

	return poll
}

func deviceStatusPollSucceeded(
	poll deviceStatusPoll,
	stereo stereoStatusPoll,
	stereoCapable bool,
) bool {
	return anyStatusFetchSucceeded(
		poll.nowPlayingErr,
		poll.nameErr,
		poll.volumeErr,
		poll.presetsErr,
		poll.sourcesErr,
		poll.bassErr,
		poll.zoneErr,
	) || poll.bassCapabilitiesOutcome == webtypes.BassCapabilitiesFetched ||
		(stereoCapable && stereo.groupErr == nil) ||
		(stereo.balanceFetched && stereo.balanceErr == nil && stereo.balance != nil)
}

func anyStatusFetchSucceeded(errors ...error) bool {
	for _, err := range errors {
		if err == nil {
			return true
		}
	}

	return false
}

func applyGroupUpdatedEvent(conn *webtypes.DeviceConnection, event *models.GroupUpdatedEvent) {
	conn.MarkEventStreamActivity(time.Now())
	conn.ApplyGroupEvent(&event.Group, time.Now())
}

func (app *WebApp) applyBalanceUpdatedEvent(
	deviceID string,
	conn *webtypes.DeviceConnection,
	event *models.BalanceUpdatedEvent,
) {
	if event == nil || !speakerConnectionEventMatches(conn, event.DeviceID) {
		return
	}

	applied := false

	conn.WithBalanceOperation(func() {
		if !app.confirmedStereoBalanceTarget(deviceID, conn) {
			return
		}

		refresh, ok := conn.BeginBalanceRefresh()
		if !ok || !confirmedStereoBalanceMaster(conn.Info().DeviceID, refresh.Group) {
			return
		}

		balance := &event.Balance
		if _, _, _, _, capabilityKnown := balance.Capability(); !capabilityKnown {
			var err error

			balance, err = conn.Client.GetBalance()
			if err != nil || balance == nil {
				return
			}
		}

		applied = app.applyBalanceReadback(deviceID, conn, refresh, balance)
	})

	if applied {
		conn.MarkEventStreamActivity(time.Now())
	}
}

// refreshZonesAfterEvent treats zoneUpdated as an invalidation hint. The raw
// event is not authoritative: the current master is queried through /getZone
// before cached topology changes. Cached masters containing the event source
// are included so a dissolve event with no new master can clear the old zone.
func (app *WebApp) refreshZonesAfterEvent(eventDeviceID, eventMasterID string) {
	candidates := map[string]struct{}{}
	if eventDeviceID != "" {
		candidates[eventDeviceID] = struct{}{}
	}

	if eventMasterID != "" {
		candidates[eventMasterID] = struct{}{}
	}

	for _, entry := range app.DeviceSnapshot() {
		status := entry.Device.Status()
		if status == nil || status.Zone == nil || !status.Zone.IsInZone(eventDeviceID) {
			continue
		}

		candidates[status.Zone.Master] = struct{}{}
	}

	for masterID := range candidates {
		app.refreshAuthoritativeZone(masterID)
	}
}

func (app *WebApp) refreshAuthoritativeZone(masterDeviceID string) {
	masterIP := app.findIPByHwID(masterDeviceID)

	master, ok := app.GetDevice(masterIP)
	if !ok || master.Client == nil {
		return
	}

	generation := master.BeginZoneRefresh()

	zone, err := master.Client.GetZone()
	if err != nil {
		log.Printf("Failed to refresh zone master %s: %v", sanitizeLog(masterDeviceID), err)
		return
	}

	if master.ApplyPolledZone(generation, masterDeviceID, zone) {
		app.BroadcastDeviceList()
	}
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

	// Capture and send under the same ordering seam used by lifecycle responses.
	if err := app.withGlobalWebSocketWrite(func(batch webSocketWriteBatch) error {
		return batch.writeJSON(conn, webtypes.WebSocketMessage{
			Type:     "device_status",
			DeviceID: deviceID,
			Data: map[string]interface{}{
				"info":   device.Info(),
				"status": device.Status(),
			},
		})
	}); err != nil {
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
		if err := app.writeDeviceWebSocketUpdate(conn, deviceID, device); err != nil {
			log.Printf("Failed to send device WebSocket update for %s: %v", sanitizeLog(deviceID), err)
			return
		}
	}
}

func (app *WebApp) writeDeviceWebSocketUpdate(
	conn webSocketWriter,
	deviceID string,
	device *webtypes.DeviceConnection,
) error {
	return app.withGlobalWebSocketWrite(func(batch webSocketWriteBatch) error {
		// Capture after taking the lifecycle ordering lock. A status frame
		// captured before a pair mutation therefore cannot follow its response.
		status := device.Status()

		if err := batch.writeMessage(conn, websocket.PingMessage, []byte{}); err != nil {
			return err
		}

		if err := batch.writeJSON(conn, webtypes.WebSocketMessage{
			Type:     "device_status",
			DeviceID: deviceID,
			Data: map[string]interface{}{
				"info":   device.Info(),
				"status": status,
			},
		}); err != nil {
			return err
		}

		if device.CurrentWebSocket() == nil || !status.WebSocketConnected {
			return nil
		}

		return batch.writeJSON(conn, webtypes.WebSocketMessage{
			Type:     "device_realtime",
			DeviceID: deviceID,
			Data: map[string]interface{}{
				"nowPlaying": status.NowPlaying,
				"volume":     status.Volume,
				"timestamp":  time.Now(),
			},
		})
	})
}
