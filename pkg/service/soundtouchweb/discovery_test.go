package soundtouchweb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

func TestDiscoverDevicesRetriesConfiguredHosts(t *testing.T) {
	var available atomic.Bool
	var infoRequests atomic.Int32

	// NewTestServer (Go 1.27) registers its own t.Cleanup(Close) instead of
	// needing a manual defer, and fails the test on a handler panic. It
	// defaults to an in-memory transport reachable only via Server.Client(),
	// but our production client.NewClient dials a real address, so Start()
	// (rather than Client()) is used here to get a real loopback listener,
	// same as the old NewServer -- see https://pkg.go.dev/net/http/httptest#NewTestServer.
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server.Start()

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

func TestResolvedDeviceIPAddressSeparatesHostnameFromReportedAddress(t *testing.T) {
	info := &models.DeviceInfo{NetworkInfo: []models.NetworkInfo{
		{Type: "SCM", IPAddress: "192.0.2.42"},
	}}

	if got := resolvedDeviceIPAddress("kitchen.local", info); got != "192.0.2.42" {
		t.Fatalf("resolved address = %q, want reported speaker address", got)
	}
}

func TestResolvedDeviceIPAddressPreservesLiteralIP(t *testing.T) {
	if got := resolvedDeviceIPAddress("192.0.2.20", &models.DeviceInfo{}); got != "192.0.2.20" {
		t.Fatalf("resolved literal address = %q, want unchanged literal", got)
	}
}

func TestRetryUntilReadyStopsAfterSuccess(t *testing.T) {
	attempts := 0
	retryUntilReady(context.Background(), time.Millisecond, func() bool {
		attempts++
		return attempts == 3
	})

	if attempts != 3 {
		t.Fatalf("attempt count = %d, want 3", attempts)
	}
}

func TestRetryUntilReadyStopsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0

	retryUntilReady(ctx, time.Hour, func() bool {
		attempts++
		cancel()

		return false
	})

	if attempts != 1 {
		t.Fatalf("attempt count = %d, want 1", attempts)
	}
}

func TestExtraDeviceHostsPresent(t *testing.T) {
	app := NewWebApp()
	app.ExtraDeviceHosts = func() ([]string, error) { return []string{"known", "", "known", "missing"}, nil }
	app.AddDevice("known", &webtypes.DeviceConnection{})
	desired, err := app.extraDeviceHostSet()
	if err != nil {
		t.Fatalf("extraDeviceHostSet() error = %v", err)
	}

	if len(desired) != 2 {
		t.Fatalf("desired host count = %d, want 2", len(desired))
	}
	if app.extraDeviceHostsPresent(desired) {
		t.Fatal("extraDeviceHostsPresent = true with a missing host")
	}

	app.AddDevice("missing", &webtypes.DeviceConnection{})
	if !app.extraDeviceHostsPresent(desired) {
		t.Fatal("extraDeviceHostsPresent = false with all hosts registered")
	}
}

func TestExtraDeviceHostSetPropagatesHookError(t *testing.T) {
	app := NewWebApp()
	wantErr := errors.New("datastore glitch")
	app.ExtraDeviceHosts = func() ([]string, error) { return nil, wantErr }

	if _, err := app.extraDeviceHostSet(); !errors.Is(err, wantErr) {
		t.Fatalf("extraDeviceHostSet() error = %v, want %v", err, wantErr)
	}
}

func TestSeedExtraDevicesSkipsKnownHosts(t *testing.T) {
	app := NewWebApp()
	app.ExtraDeviceHosts = func() ([]string, error) { return []string{"known"}, nil }
	lastSeen := time.Unix(123, 0)
	conn := &webtypes.DeviceConnection{LastSeen: lastSeen}
	app.AddDevice("known", conn)

	app.SeedExtraDevices()

	if !conn.LastSeen.Equal(lastSeen) {
		t.Fatalf("known host LastSeen changed from %s to %s", lastSeen, conn.LastSeen)
	}
}

// TestSeedExtraDevicesUntilReadyRetriesOnHookError covers the code-review
// finding that a hook error (e.g. a transient datastore read failure) must
// not be treated as "zero hosts persisted", which would make the readiness
// check trivially pass and end the retry window immediately.
func TestSeedExtraDevicesUntilReadyRetriesOnHookError(t *testing.T) {
	var calls atomic.Int32

	app := NewWebApp()
	app.ExtraDeviceHosts = func() ([]string, error) {
		n := calls.Add(1)
		if n < 3 {
			return nil, errors.New("datastore glitch")
		}

		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	app.SeedExtraDevicesUntilReady(ctx, time.Millisecond)

	if got := calls.Load(); got < 3 {
		t.Fatalf("hook call count = %d, want at least 3 (kept retrying past the errors)", got)
	}
}

// TestSeedExtraDevicesPrunesHostRemovedAfterEarlierAttempt covers the
// code-review finding that pruning must consider the whole registry, not
// only hosts inserted during the current call: a host registered by an
// earlier seed call that later falls out of ExtraDeviceHosts must still be
// pruned by a later call, and this must hold for the plain SeedExtraDevices
// path too, not just the bounded retry loop.
func TestSeedExtraDevicesPrunesHostRemovedAfterEarlierAttempt(t *testing.T) {
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<info deviceID="TESTDEVICE"><name>Stale speaker</name><type>SoundTouch 10</type></info>`))
	}))
	server.Start()

	host := strings.TrimPrefix(server.URL, "http://")

	var stillDesired atomic.Bool
	stillDesired.Store(true)

	app := NewWebApp()
	app.ExtraDeviceHosts = func() ([]string, error) {
		if stillDesired.Load() {
			return []string{host}, nil
		}

		return nil, nil
	}

	// First call registers the host.
	app.SeedExtraDevices()
	if _, ok := app.GetDevice(host); !ok {
		t.Fatalf("host %s was not registered on the first seed call", host)
	}

	// Simulate the device being removed from the datastore in between calls.
	stillDesired.Store(false)

	// A later plain SeedExtraDevices call (as triggered by
	// SetDevicesChangedHook or the manual /api/control/discover route) must
	// still prune it, not just the bounded retry loop.
	app.SeedExtraDevices()
	if _, ok := app.GetDevice(host); ok {
		t.Fatal("stale host was not pruned by a later SeedExtraDevices call")
	}
}

// TestSeedExtraDevicesSerializesConcurrentRuns covers the code-review finding
// that the bounded startup retry loop and a devices-changed-hook-triggered
// SeedExtraDevices call must not issue concurrent probes to the same
// still-offline host.
func TestSeedExtraDevicesSerializesConcurrentRuns(t *testing.T) {
	var infoRequests atomic.Int32
	release := make(chan struct{})

	// Only /info blocks and counts. A successful registration spawns its own
	// background status-update request (see the comment below), which must
	// not be mistaken for a second concurrent seed probe.
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			http.NotFound(w, r)
			return
		}

		infoRequests.Add(1)
		<-release // block until the test lets the handler respond

		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<info deviceID="TESTDEVICE"><name>Slow speaker</name><type>SoundTouch 10</type></info>`))
	}))
	server.Start()

	host := strings.TrimPrefix(server.URL, "http://")

	app := NewWebApp()
	app.ExtraDeviceHosts = func() ([]string, error) { return []string{host}, nil }

	done := make(chan struct{}, 2)
	go func() { app.SeedExtraDevices(); done <- struct{}{} }()
	go func() { app.SeedExtraDevices(); done <- struct{}{} }()

	// Give both goroutines a moment to reach the handler if they were going
	// to run concurrently, then let the handler(s) respond.
	time.Sleep(50 * time.Millisecond)
	close(release)

	<-done
	<-done

	if got := infoRequests.Load(); got != 1 {
		t.Fatalf("/info request count = %d, want 1 (concurrent seeds were not serialized)", got)
	}

	// AddDeviceByHost spawns a one-shot status-update goroutine and a 30s-
	// ticker poll loop on successful registration (see
	// TestDiscoverDevicesRetriesConfiguredHosts). Give them a moment to
	// finish before the deferred server.Close() runs, so a still-in-flight
	// request against the closing httptest server doesn't produce log noise
	// or -race flakiness.
	time.Sleep(50 * time.Millisecond)
}

func TestRemoveDeviceIfMatchKeepsReplacement(t *testing.T) {
	app := NewWebApp()
	original := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{})
	replacement := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{})
	app.AddDevice("speaker", original)
	app.RemoveDevice("speaker")
	app.AddDevice("speaker", replacement)

	if app.removeDeviceIfMatch("speaker", original) {
		t.Fatal("removeDeviceIfMatch removed a replacement connection")
	}

	got, ok := app.GetDevice("speaker")
	if !ok || got != replacement {
		t.Fatal("replacement connection was not preserved")
	}

	app.RemoveDevice("speaker")
}
