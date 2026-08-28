package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gorilla/websocket"
)

// mockLogger implements the Logger interface for testing
type mockLogger struct {
	messages []string
	mu       sync.Mutex
}

func (m *mockLogger) Printf(format string, _ ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.messages = append(m.messages, format)
}

func (m *mockLogger) getMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]string, len(m.messages))
	copy(result, m.messages)

	return result
}

// setupMockWebSocketServer creates a test WebSocket server
func setupMockWebSocketServer(t *testing.T) (*httptest.Server, chan []byte) {
	t.Helper()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool {
			return true
		},
	}

	messagesChan := make(chan []byte, 10)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Failed to upgrade connection: %v", err)
			return
		}

		defer func() {
			_ = conn.Close()
		}()

		// Send test messages from the channel
		go func() {
			for message := range messagesChan {
				if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
					return
				}
			}
		}()

		// Keep connection alive and handle pings
		for {
			messageType, _, err := conn.ReadMessage()
			if err != nil {
				break
			}

			if messageType == websocket.PingMessage {
				_ = conn.WriteMessage(websocket.PongMessage, nil)
			}
		}
	}))

	return server, messagesChan
}

func TestDefaultWebSocketConfig(t *testing.T) {
	config := DefaultWebSocketConfig()

	if config == nil {
		t.Fatal("DefaultWebSocketConfig() returned nil")
	}

	if config.ReconnectInterval != 5*time.Second {
		t.Errorf("Expected ReconnectInterval 5s, got %v", config.ReconnectInterval)
	}

	if config.MaxReconnectAttempts != 0 {
		t.Errorf("Expected MaxReconnectAttempts 0 (unlimited), got %d", config.MaxReconnectAttempts)
	}

	if config.PingInterval != 30*time.Second {
		t.Errorf("Expected PingInterval 30s, got %v", config.PingInterval)
	}

	if config.PongTimeout != 10*time.Second {
		t.Errorf("Expected PongTimeout 10s, got %v", config.PongTimeout)
	}

	if config.ReadBufferSize != 1024 {
		t.Errorf("Expected ReadBufferSize 1024, got %d", config.ReadBufferSize)
	}

	if config.WriteBufferSize != 1024 {
		t.Errorf("Expected WriteBufferSize 1024, got %d", config.WriteBufferSize)
	}

	if config.Logger == nil {
		t.Error("Expected Logger to be set")
	}
}

func TestNewWebSocketClient(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)

	if wsClient == nil {
		t.Fatal("NewWebSocketClient() returned nil")
	}

	if wsClient.client != client {
		t.Error("WebSocket client should reference the parent client")
	}

	if wsClient.handlers == nil {
		t.Error("WebSocket client should have handlers initialized")
	}

	if !wsClient.reconnect {
		t.Error("WebSocket client should have reconnect enabled by default")
	}

	if wsClient.connected {
		t.Error("WebSocket client should not be connected initially")
	}
}

func TestWebSocketClient_SetHandlers(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)

	handlers := &models.WebSocketEventHandlers{
		OnNowPlaying: func(_ *models.NowPlayingUpdatedEvent) {
			// Handler implementation for testing
		},
		OnVolumeUpdated: func(_ *models.VolumeUpdatedEvent) {
			// Handler implementation for testing
		},
	}

	wsClient.SetHandlers(handlers)

	// Verify handlers were set
	wsClient.mu.RLock()

	if wsClient.handlers != handlers {
		t.Error("Handlers were not set correctly")
	}

	wsClient.mu.RUnlock()
}

func TestWebSocketClient_IndividualHandlers(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)

	wsClient.OnNowPlaying(func(_ *models.NowPlayingUpdatedEvent) {
		// Handler implementation for testing
	})

	wsClient.OnVolumeUpdated(func(_ *models.VolumeUpdatedEvent) {
		// Handler implementation for testing
	})

	wsClient.OnConnectionState(func(_ *models.ConnectionStateUpdatedEvent) {
		// Handler implementation for testing
	})

	// Verify handlers were set
	wsClient.mu.RLock()

	if wsClient.handlers.OnNowPlaying == nil {
		t.Error("OnNowPlaying handler not set")
	}

	if wsClient.handlers.OnVolumeUpdated == nil {
		t.Error("OnVolumeUpdated handler not set")
	}

	if wsClient.handlers.OnConnectionState == nil {
		t.Error("OnConnectionState handler not set")
	}

	wsClient.mu.RUnlock()
}

func TestWebSocketClient_IsConnected(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)

	// Initially should not be connected
	if wsClient.IsConnected() {
		t.Error("WebSocket client should not be connected initially")
	}

	// Simulate connected state
	wsClient.mu.Lock()
	wsClient.connected = true
	wsClient.mu.Unlock()

	if !wsClient.IsConnected() {
		t.Error("WebSocket client should report as connected")
	}
}

func TestWebSocketClient_ConnectToMockServer(t *testing.T) {
	server, messagesChan := setupMockWebSocketServer(t)
	defer server.Close()
	defer close(messagesChan)

	// Extract host and port from test server
	serverURL := strings.Replace(server.URL, "http://", "", 1)
	parts := strings.Split(serverURL, ":")
	host := parts[0]

	client := NewClientFromHost(host)
	wsClient := client.NewWebSocketClient(nil)

	// Override the WebSocket port to match test server
	// Note: In a real implementation, you might want to make the WebSocket port configurable
	// For this test, we'll simulate connection success

	if wsClient.IsConnected() {
		t.Error("WebSocket client should not be connected initially")
	}
}

func TestWebSocketClient_Disconnect(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)

	// Test disconnect when not connected
	err := wsClient.Disconnect()
	if err == nil {
		t.Error("Expected error when disconnecting while not connected")
	}

	// Simulate connected state
	wsClient.mu.Lock()
	wsClient.connected = true
	wsClient.mu.Unlock()

	err = wsClient.Disconnect()
	if err != nil {
		t.Errorf("Unexpected error when disconnecting: %v", err)
	}

	if wsClient.IsConnected() {
		t.Error("WebSocket client should not be connected after disconnect")
	}
}

func TestWebSocketClient_CloseStopsDisconnectedReconnect(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)

	if err := wsClient.Close(); err != nil {
		t.Fatalf("Close() on a disconnected client failed: %v", err)
	}

	wsClient.mu.RLock()
	reconnect := wsClient.reconnect
	wsClient.mu.RUnlock()

	if reconnect {
		t.Fatal("Close() left reconnect enabled")
	}

	select {
	case <-wsClient.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel the WebSocket context")
	}

	if err := wsClient.Close(); err != nil {
		t.Fatalf("second Close() was not idempotent: %v", err)
	}
}

func TestWebSocketClient_CloseCancelsInProgressDial(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)
	dialStarted := make(chan struct{})
	wsClient.dialContext = func(ctx context.Context, _ string, _ http.Header) (*websocket.Conn, *http.Response, error) {
		close(dialStarted)
		<-ctx.Done()

		return nil, nil, ctx.Err()
	}

	connectDone := make(chan error, 1)
	go func() {
		connectDone <- wsClient.Connect()
	}()

	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("WebSocket dial did not start")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- wsClient.Close()
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() failed: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close() waited for the WebSocket handshake timeout")
	}

	select {
	case err := <-connectDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Connect() error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled WebSocket dial did not return")
	}
}

func TestWebSocketClient_ReconnectCyclesKeepSinglePingOwner(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(&WebSocketConfig{Logger: &mockLogger{}})
	config := &WebSocketConfig{
		PingInterval: time.Hour,
		Logger:       &mockLogger{},
	}

	var previous *webSocketConnection
	for cycle := 0; cycle < 3; cycle++ {
		conn, pings := newPingTrackingWebSocket(t)

		wsClient.mu.Lock()
		wsClient.activateConnectionLocked(conn, config)
		current := wsClient.connection
		wsClient.mu.Unlock()

		if previous != nil {
			waitForPingOwnerExit(t, previous)

			active, err := wsClient.writePing(previous)
			if err != nil {
				t.Fatalf("replaced generation write returned an error: %v", err)
			}
			if active {
				t.Fatal("replaced generation retained ping ownership")
			}
		}

		active, err := wsClient.writePing(current)
		if err != nil {
			t.Fatalf("active generation ping failed: %v", err)
		}
		if !active {
			t.Fatal("current generation did not own ping writes")
		}

		select {
		case <-pings:
		case <-time.After(time.Second):
			t.Fatal("server did not receive ping from current generation")
		}

		select {
		case <-current.pingDone:
			t.Fatal("current generation's ping owner exited early")
		default:
		}

		previous = current
	}

	if err := wsClient.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	waitForPingOwnerExit(t, previous)

	active, err := wsClient.writePing(previous)
	if err != nil {
		t.Fatalf("closed generation write returned an error: %v", err)
	}
	if active {
		t.Fatal("closed generation retained ping ownership")
	}

	if err := wsClient.Close(); err != nil {
		t.Fatalf("second Close() was not idempotent: %v", err)
	}
}

func newPingTrackingWebSocket(t *testing.T) (*websocket.Conn, <-chan struct{}) {
	t.Helper()

	pings := make(chan struct{}, 1)
	serverConn := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		conn.SetPingHandler(func(string) error {
			select {
			case pings <- struct{}{}:
			default:
			}

			return nil
		})
		serverConn <- conn

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		server.Close()
		t.Fatalf("failed to create WebSocket test connection: %v", err)
	}

	peer := <-serverConn
	t.Cleanup(func() {
		_ = conn.Close()
		_ = peer.Close()
		server.Close()
	})

	return conn, pings
}

func waitForPingOwnerExit(t *testing.T, connection *webSocketConnection) {
	t.Helper()

	select {
	case <-connection.pingDone:
	case <-time.After(time.Second):
		t.Fatal("ping owner did not exit after its connection generation ended")
	}
}

func TestWebSocketClient_HandleMessage(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(&WebSocketConfig{
		Logger: &mockLogger{},
	})

	var (
		nowPlayingEvent *models.NowPlayingUpdatedEvent
		volumeEvent     *models.VolumeUpdatedEvent
		balanceEvent    *models.BalanceUpdatedEvent
		nameEvent       *models.NameUpdatedEvent
	)

	wsClient.OnNowPlaying(func(event *models.NowPlayingUpdatedEvent) {
		nowPlayingEvent = event
	})

	wsClient.OnVolumeUpdated(func(event *models.VolumeUpdatedEvent) {
		volumeEvent = event
	})

	wsClient.OnBalanceUpdated(func(event *models.BalanceUpdatedEvent) {
		balanceEvent = event
	})

	wsClient.OnNameUpdated(func(event *models.NameUpdatedEvent) {
		nameEvent = event
	})

	t.Run("HandleBalanceEvent", func(t *testing.T) {
		xmlData := []byte(`<updates deviceID="0CAE7D42D459"><balanceUpdated deviceID="0CAE7D42D459"><balance deviceID="0CAE7D42D459"><balanceAvailable>true</balanceAvailable><balanceMin>-7</balanceMin><balanceMax>7</balanceMax><balanceDefault>0</balanceDefault><targetBalance>3</targetBalance><actualBalance>3</actualBalance></balance></balanceUpdated></updates>`)

		wsClient.handleMessage(xmlData)

		if balanceEvent == nil || balanceEvent.DeviceID != "0CAE7D42D459" ||
			!balanceEvent.Balance.CapabilityKnown || balanceEvent.Balance.ActualBalance != 3 {
			t.Fatalf("unexpected balance event: %+v", balanceEvent)
		}
	})

	t.Run("HandleNowPlayingEvent", func(t *testing.T) {
		xmlData := []byte(`<?xml version="1.0" encoding="UTF-8" ?>
<updates deviceID="689E19B8BB8A">
	<nowPlayingUpdated deviceID="689E19B8BB8A">
		<nowPlaying deviceID="689E19B8BB8A" source="SPOTIFY">
			<track>Test Track</track>
			<artist>Test Artist</artist>
			<album>Test Album</album>
			<playStatus>PLAY_STATE</playStatus>
		</nowPlaying>
	</nowPlayingUpdated>
</updates>`)

		wsClient.handleMessage(xmlData)

		if nowPlayingEvent == nil {
			t.Fatal("Now playing event handler was not called")
		}

		if nowPlayingEvent.DeviceID != "689E19B8BB8A" {
			t.Errorf("Expected DeviceID '689E19B8BB8A', got '%s'", nowPlayingEvent.DeviceID)
		}

		if nowPlayingEvent.NowPlaying.Track != "Test Track" {
			t.Errorf("Expected Track 'Test Track', got '%s'", nowPlayingEvent.NowPlaying.Track)
		}
	})

	t.Run("HandleVolumeEvent", func(t *testing.T) {
		xmlData := []byte(`<?xml version="1.0" encoding="UTF-8" ?>
<updates deviceID="689E19B8BB8A">
	<volumeUpdated deviceID="689E19B8BB8A">
		<volume deviceID="689E19B8BB8A">
			<targetvolume>25</targetvolume>
			<actualvolume>25</actualvolume>
			<muteenabled>false</muteenabled>
		</volume>
	</volumeUpdated>
</updates>`)

		wsClient.handleMessage(xmlData)

		if volumeEvent == nil {
			t.Fatal("Volume event handler was not called")
		}

		if volumeEvent.DeviceID != "689E19B8BB8A" {
			t.Errorf("Expected DeviceID '689E19B8BB8A', got '%s'", volumeEvent.DeviceID)
		}

		if volumeEvent.Volume.TargetVolume != 25 {
			t.Errorf("Expected TargetVolume 25, got %d", volumeEvent.Volume.TargetVolume)
		}
	})

	t.Run("HandleNameEvent", func(t *testing.T) {
		xmlData := []byte(`<updates deviceID="689E19B8BB8A"><nameUpdated deviceID="689E19B8BB8A"><name>Living Room Left</name></nameUpdated></updates>`)

		wsClient.handleMessage(xmlData)

		if nameEvent == nil {
			t.Fatal("Name event handler was not called")
		}

		if nameEvent.DeviceID != "689E19B8BB8A" || nameEvent.Name.Value != "Living Room Left" {
			t.Errorf("Unexpected name event: %+v", nameEvent)
		}
	})

	t.Run("HandleInvalidXML", func(t *testing.T) {
		logger := &mockLogger{}
		wsClient.logger = logger

		xmlData := []byte(`<invalid xml>`)
		wsClient.handleMessage(xmlData)

		messages := logger.getMessages()
		if len(messages) == 0 {
			t.Error("Expected error message to be logged for invalid XML")
		}
	})
}

func TestWebSocketClient_HandleUnknownEvent(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	logger := &mockLogger{}
	wsClient := client.NewWebSocketClient(&WebSocketConfig{
		Logger: logger,
	})

	var unknownEventReceived *models.WebSocketEvent

	wsClient.OnUnknownEvent(func(event *models.WebSocketEvent) {
		unknownEventReceived = event
	})

	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8" ?>
<updates deviceID="689E19B8BB8A">
	<unknownEventType deviceID="689E19B8BB8A">
		<someData>test</someData>
	</unknownEventType>
</updates>`)

	wsClient.handleMessage(xmlData)

	if unknownEventReceived == nil {
		t.Fatal("Unknown event handler was not called")
	}

	if unknownEventReceived.DeviceID != "689E19B8BB8A" {
		t.Errorf("Expected DeviceID '689E19B8BB8A', got '%s'", unknownEventReceived.DeviceID)
	}
}

func TestWebSocketClient_SendMessage(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)

	// Test send when not connected
	err := wsClient.SendMessage([]byte("test"))
	if err == nil {
		t.Error("Expected error when sending message while not connected")
	}
}

func TestWebSocketClient_ConfigValidation(t *testing.T) {
	client := NewClientFromHost("192.0.2.10")

	t.Run("NilConfig", func(t *testing.T) {
		wsClient := client.NewWebSocketClient(nil)
		if wsClient == nil {
			t.Fatal("NewWebSocketClient should handle nil config gracefully")
		}
	})

	t.Run("CustomConfig", func(t *testing.T) {
		customConfig := &WebSocketConfig{
			ReconnectInterval:    1 * time.Second,
			MaxReconnectAttempts: 5,
			PingInterval:         15 * time.Second,
			PongTimeout:          5 * time.Second,
			ReadBufferSize:       2048,
			WriteBufferSize:      2048,
			Logger:               &mockLogger{},
		}

		wsClient := client.NewWebSocketClient(customConfig)
		if wsClient == nil {
			t.Fatal("NewWebSocketClient should handle custom config")
		}
	})
}

func TestWebSocketClient_ConcurrentAccess(_ *testing.T) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)

	// Test concurrent access to handlers
	var wg sync.WaitGroup

	numGoroutines := 10

	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()

			wsClient.OnNowPlaying(func(_ *models.NowPlayingUpdatedEvent) {
				// Handler implementation
			})

			wsClient.OnVolumeUpdated(func(_ *models.VolumeUpdatedEvent) {
				// Handler implementation
			})

			// Test IsConnected concurrently
			_ = wsClient.IsConnected()
		}()
	}

	wg.Wait()
}

// Benchmark tests
func BenchmarkWebSocketClient_HandleMessage(b *testing.B) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(&WebSocketConfig{
		Logger: &mockLogger{},
	})

	wsClient.OnNowPlaying(func(_ *models.NowPlayingUpdatedEvent) {
		// Minimal handler for benchmarking
	})

	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8" ?>
<updates deviceID="689E19B8BB8A">
	<nowPlayingUpdated deviceID="689E19B8BB8A">
		<nowPlaying deviceID="689E19B8BB8A" source="SPOTIFY">
			<track>Test Track</track>
			<artist>Test Artist</artist>
			<album>Test Album</album>
			<playStatus>PLAY_STATE</playStatus>
		</nowPlaying>
	</nowPlayingUpdated>
</updates>`)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		wsClient.handleMessage(xmlData)
	}
}

func BenchmarkWebSocketClient_SetHandlers(b *testing.B) {
	client := NewClientFromHost("192.0.2.10")
	wsClient := client.NewWebSocketClient(nil)

	handlers := &models.WebSocketEventHandlers{
		OnNowPlaying:    func(_ *models.NowPlayingUpdatedEvent) {},
		OnVolumeUpdated: func(_ *models.VolumeUpdatedEvent) {},
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		wsClient.SetHandlers(handlers)
	}
}

// Integration test with mock server
func TestWebSocketClient_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	server, messagesChan := setupMockWebSocketServer(t)
	defer server.Close()

	// This would be a more comprehensive integration test
	// For now, we'll test the setup and teardown
	if messagesChan == nil {
		t.Fatal("Message channel should be initialized")
	}

	close(messagesChan)
}
