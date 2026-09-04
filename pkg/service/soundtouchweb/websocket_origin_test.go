package soundtouchweb

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWebSocketOriginPolicy(t *testing.T) {
	app := NewWebApp()
	server, _, release := newTestWebSocketServer(t, app)
	defer func() {
		release()
		server.Close()
	}()

	webSocketURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// A same-hostname origin with a different port, simulating a reverse
	// proxy whose forwarded Host header drops the public port (nginx's
	// $host) while the browser's Origin keeps it -- see
	// sameHostIgnoringPort's doc comment.
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	if serverURL.Port() == "18443" {
		t.Fatal("test server happened to bind the port this test uses as a deliberately different one")
	}

	mismatchedPortOrigin := serverURL.Scheme + "://" + serverURL.Hostname() + ":18443"

	tests := []struct {
		name       string
		origin     string
		wantStatus int
	}{
		{name: "originless non-browser client", wantStatus: http.StatusSwitchingProtocols},
		{name: "same origin", origin: server.URL, wantStatus: http.StatusSwitchingProtocols},
		{name: "same host, mismatched port (reverse proxy)", origin: mismatchedPortOrigin, wantStatus: http.StatusSwitchingProtocols},
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

func TestSameHostIgnoringPort(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "identical host and port", a: "example.com:8443", b: "example.com:8443", want: true},
		{name: "same host, mismatched port", a: "example.com:8443", b: "example.com", want: true},
		{name: "same host, no ports either side", a: "example.com", b: "example.com", want: true},
		{name: "different host, same port", a: "example.com:8443", b: "attacker.example:8443", want: false},
		{name: "different host, no ports", a: "example.com", b: "attacker.example", want: false},
		{name: "case-insensitive host", a: "Example.com", b: "example.com", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sameHostIgnoringPort(test.a, test.b); got != test.want {
				t.Errorf("sameHostIgnoringPort(%q, %q) = %v, want %v", test.a, test.b, got, test.want)
			}
		})
	}
}

func TestCheckWebSocketOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{name: "no origin header (non-browser client)", host: "example.com:8443", want: true},
		{name: "same host, mismatched port (reverse proxy)", origin: "https://example.com:8443", host: "example.com", want: true},
		{name: "different host", origin: "https://attacker.example", host: "example.com:8443", want: false},
		{name: "malformed origin", origin: "://not a url", host: "example.com", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := &http.Request{Host: test.host, Header: http.Header{}}
			if test.origin != "" {
				r.Header.Set("Origin", test.origin)
			}

			if got := checkWebSocketOrigin(r); got != test.want {
				t.Errorf("checkWebSocketOrigin(origin=%q, host=%q) = %v, want %v", test.origin, test.host, got, test.want)
			}
		})
	}
}
