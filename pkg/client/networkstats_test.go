package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientGetNetworkStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/netStats" || r.Method != http.MethodGet {
			t.Fatalf("request = %s %s, want GET /netStats", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`<network-data><devices><device><interfaces><interface>` +
			`<name>eth0</name><running>true</running><kind>Wireless</kind>` +
			`<bindings><ipv4address>192.0.2.10</ipv4address></bindings><ssid>Test WiFi</ssid><rssi>Good</rssi>` +
			`</interface></interfaces></device></devices></network-data>`))
	}))
	defer server.Close()

	stats, err := createTestClient(server.URL).GetNetworkStats()
	if err != nil {
		t.Fatalf("GetNetworkStats: %v", err)
	}
	iface := stats.FindRunningWireless("192.0.2.10", "Test WiFi")
	if iface == nil || iface.Name != "eth0" || iface.RSSI != "Good" {
		t.Fatalf("unexpected network stats: %+v", stats)
	}
}

func TestClientGetNetworkStatsErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantError  string
	}{
		{name: "temporary failure", statusCode: http.StatusServiceUnavailable, body: "busy", wantError: "status 503"},
		{name: "malformed XML", statusCode: http.StatusOK, body: `<network-data>`, wantError: "failed to unmarshal XML response"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			stats, err := createTestClient(server.URL).GetNetworkStats()
			if err == nil || stats != nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("GetNetworkStats() = (%+v, %v), want error containing %q", stats, err, test.wantError)
			}
		})
	}
}
