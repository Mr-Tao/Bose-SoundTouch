package soundtouchweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
}
