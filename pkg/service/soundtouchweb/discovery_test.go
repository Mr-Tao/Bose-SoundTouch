package soundtouchweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoverDevicesRetriesConfiguredHosts(t *testing.T) {
	var available atomic.Bool
	var infoRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			http.NotFound(w, r)
			return
		}

		infoRequests.Add(1)
		if !available.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<info deviceID="TESTDEVICE"><name>Configured speaker</name><type>SoundTouch 10</type></info>`))
	}))
	defer server.Close()

	t.Setenv("UPNP_ENABLED", "false")
	t.Setenv("MDNS_ENABLED", "false")
	t.Setenv("PREFERRED_DEVICES", "")

	configuredHost := strings.TrimPrefix(server.URL, "http://")
	discoveryService := NewDiscoveryService("", configuredHost)
	app := NewWebApp()

	app.DiscoverDevices(context.Background(), discoveryService)
	if got := app.DeviceCount(); got != 0 {
		t.Fatalf("device count after offline probe = %d, want 0", got)
	}

	available.Store(true)
	app.DiscoverDevices(context.Background(), discoveryService)
	if got := app.DeviceCount(); got != 1 {
		t.Fatalf("device count after retry = %d, want 1", got)
	}
	if got := infoRequests.Load(); got != 2 {
		t.Fatalf("/info request count = %d, want 2", got)
	}

	if !app.RemoveDevice(configuredHost) {
		t.Fatal("configured device was not registered under its host")
	}

	// AddDeviceByHost spawns a one-shot status-update goroutine and a 30s-
	// ticker poll loop on successful registration. RemoveDevice signals the
	// ticker loop to exit via conn.Done() but doesn't wait for it to actually
	// observe the close, and the one-shot goroutine has no cancellation at
	// all. Give them a moment to finish before the deferred server.Close()
	// runs, so a still-in-flight request against the closing httptest server
	// doesn't produce log noise or -race flakiness.
	time.Sleep(50 * time.Millisecond)
}

// TestClassifySource covers the case the test above can't reach without a
// real mDNS/UPnP sweep: mergeDeviceData joins discovery methods with "+"
// when a configured host is also found via mDNS/UPnP in the same pass (see
// pkg/discovery/unified.go), so DiscoveryMethod is not always exactly
// "Configuration" for a manually configured device.
func TestClassifySource(t *testing.T) {
	tests := []struct {
		discoveryMethod string
		want            string
	}{
		{"Configuration", "manual"},
		{"Configuration+mDNS/Bonjour", "manual"},
		{"mDNS/Bonjour+Configuration", "manual"},
		{"mDNS/Bonjour", "discovered"},
		{"SSDP/UPnP", "discovered"},
		{"", "discovered"},
	}

	for _, tt := range tests {
		if got := classifySource(tt.discoveryMethod); got != tt.want {
			t.Errorf("classifySource(%q) = %q, want %q", tt.discoveryMethod, got, tt.want)
		}
	}
}
