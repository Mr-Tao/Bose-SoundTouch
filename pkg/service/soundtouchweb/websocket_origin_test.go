package soundtouchweb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWebSocketOriginPolicy(t *testing.T) {
	app := NewWebApp()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := app.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		_ = conn.Close()
	}))
	defer server.Close()

	webSocketURL := "ws" + strings.TrimPrefix(server.URL, "http")
	tests := []struct {
		name       string
		origin     string
		wantStatus int
	}{
		{name: "originless non-browser client", wantStatus: http.StatusSwitchingProtocols},
		{name: "same origin", origin: server.URL, wantStatus: http.StatusSwitchingProtocols},
		{name: "cross origin", origin: "https://attacker.example", wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{}
			if test.origin != "" {
				header.Set("Origin", test.origin)
			}

			conn, response, err := websocket.DefaultDialer.Dial(webSocketURL, header)
			if response != nil {
				defer response.Body.Close()
			}

			if test.wantStatus == http.StatusSwitchingProtocols {
				if err != nil {
					t.Fatalf("WebSocket handshake failed: %v", err)
				}

				if conn == nil {
					t.Fatal("WebSocket handshake returned no connection")
				}

				_ = conn.Close()

				return
			}

			if err == nil {
				_ = conn.Close()
				t.Fatal("cross-origin WebSocket handshake unexpectedly succeeded")
			}

			if response == nil {
				t.Fatal("rejected WebSocket handshake returned no HTTP response")
			}

			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
		})
	}
}
