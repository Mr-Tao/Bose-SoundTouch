package soundtouchweb

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/config"
	"github.com/gesellix/bose-soundtouch/pkg/discovery"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/gesellix/bose-soundtouch/pkg/speaker"
)

// NewDiscoveryService loads config and returns a unified discovery service
// preconfigured for the web UI's use (10 s discovery timeout, cache on).
// When discoveryInterface is non-empty, mDNS/UPnP are pinned to that NIC.
// configuredHosts (e.g. from --devices) are folded into cfg.PreferredDevices
// alongside any already loaded from PREFERRED_DEVICES (deduplicated by
// host), so they're retried on every subsequent DiscoverDevices pass, not
// just once at startup -- a host that's offline now still gets picked up
// once it comes online.
func NewDiscoveryService(discoveryInterface string, configuredHosts ...string) *discovery.UnifiedDiscoveryService {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Printf("Failed to load config: %v, using defaults", err)

		cfg = config.DefaultConfig()
	}

	cfg.DiscoveryTimeout = 10 * time.Second
	cfg.CacheEnabled = true

	if discoveryInterface != "" {
		cfg.DiscoveryInterface = discoveryInterface
	}

	existingHosts := make(map[string]bool, len(cfg.PreferredDevices))
	for _, d := range cfg.PreferredDevices {
		existingHosts[d.Host] = true
	}

	for _, host := range configuredHosts {
		if host == "" || existingHosts[host] {
			continue
		}

		cfg.PreferredDevices = append(cfg.PreferredDevices, config.DeviceConfig{
			Host: host,
			Port: speaker.HTTPPort,
		})
		existingHosts[host] = true
	}

	return discovery.NewUnifiedDiscoveryService(cfg)
}

// AddDeviceByHost registers a SoundTouch device with the WebApp by fetching
// its /info and creating a DeviceConnection. The source label
// ("manual" or "discovered") appears in log lines so the operator can
// tell apart entries that came from --devices from those found via
// mDNS/UPnP. If the host is already known, the existing entry's
// LastSeen is bumped and the function returns without re-fetching.
func (app *WebApp) AddDeviceByHost(host string, port int, source string) {
	app.addDeviceByHost(host, port, source)
}

func (app *WebApp) addDeviceByHost(host string, port int, source string) *webtypes.DeviceConnection {
	// Fast path: skip the network call if we already know this host.
	if app.TouchDevice(host) {
		return nil
	}

	c := client.NewClient(&client.Config{
		Host:    host,
		Port:    port,
		Timeout: 10 * time.Second,
	})

	info, err := c.GetDeviceInfo()
	if err != nil {
		log.Printf("Failed to fetch device info from %s (%s): %v", sanitizeLog(host), sanitizeLog(source), err)
		return nil
	}

	// Ensure IPAddress is set for the web UI
	if info.IPAddress == "" {
		info.IPAddress = host
	}

	conn := webtypes.NewDeviceConnection(c, info)
	if !app.AddDevice(host, conn) {
		// Lost a race — another goroutine inserted the same host
		// between TouchDevice and AddDevice. AddDevice bumped LastSeen
		// on the existing entry; discard our conn.
		return nil
	}

	go app.UpdateDeviceStatus(host, conn)

	// Poll via HTTP every 30 s as a fallback for WebSocket events that the
	// speaker does not emit (e.g. Spotify Connect track changes) and for the
	// window between a WS disconnect and its reconnect.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				app.UpdateDeviceStatus(host, conn)
			case <-conn.Done():
				return
			}
		}
	}()

	log.Printf("Added %s device %s (%s) at %s:%d", sanitizeLog(source), sanitizeLog(info.Name), sanitizeLog(info.Type), sanitizeLog(host), port)

	return conn
}

// SeedExtraDevices registers any devices reported by the ExtraDeviceHosts hook
// (if set) via AddDeviceByHost. Idempotent: already-known hosts are skipped.
// Used by the embedded build to surface the service datastore's devices even
// when network discovery is disabled; a no-op for standalone soundtouch-player.
//
// Hosts are probed concurrently: AddDeviceByHost makes a blocking /info call
// (up to its 10 s timeout) for each unknown host, so an offline speaker in the
// datastore would otherwise stall the whole seed for 10 s, serially. Fanning
// out bounds the cost to roughly a single timeout regardless of how many
// devices are offline. AddDeviceByHost is registry-safe under concurrency.
func (app *WebApp) SeedExtraDevices() {
	app.seedExtraDevices()
}

type seededExtraDevice struct {
	host string
	conn *webtypes.DeviceConnection
}

func (app *WebApp) seedExtraDevices() []seededExtraDevice {
	if app.ExtraDeviceHosts == nil {
		return nil
	}

	hosts := app.extraDeviceHosts()
	inserted := make(chan seededExtraDevice, len(hosts))

	var wg sync.WaitGroup

	for _, host := range hosts {
		if _, ok := app.GetDevice(host); ok {
			continue
		}

		wg.Add(1)

		go func(h string) {
			defer wg.Done()

			if conn := app.addDeviceByHost(h, 8090, "service-store"); conn != nil {
				inserted <- seededExtraDevice{host: h, conn: conn}
			}
		}(host)
	}

	wg.Wait()
	close(inserted)

	added := make([]seededExtraDevice, 0, len(inserted))
	for device := range inserted {
		added = append(added, device)
	}

	return added
}

// SeedExtraDevicesUntilReady retries only the hosts returned by
// ExtraDeviceHosts until all of them have been registered or ctx expires. It
// does not run mDNS or UPnP discovery. This gives embedded deployments a
// bounded way to recover when their persisted speakers are not yet routable
// while the service is starting.
func (app *WebApp) SeedExtraDevicesUntilReady(ctx context.Context, retryInterval time.Duration) {
	retryUntilReady(ctx, retryInterval, func() bool {
		inserted := app.seedExtraDevices()
		desired := app.extraDeviceHostSet()

		for _, device := range inserted {
			if _, ok := desired[device.host]; !ok {
				app.removeDeviceIfMatch(device.host, device.conn)
			}
		}

		if len(inserted) > 0 {
			app.BroadcastDeviceList()
		}

		return app.extraDeviceHostsPresent(app.extraDeviceHostSet())
	})
}

func (app *WebApp) extraDeviceHosts() []string {
	if app.ExtraDeviceHosts == nil {
		return nil
	}

	hosts := make([]string, 0)
	seen := make(map[string]struct{})

	for _, host := range app.ExtraDeviceHosts() {
		if host == "" {
			continue
		}

		if _, ok := seen[host]; ok {
			continue
		}

		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}

	return hosts
}

func (app *WebApp) extraDeviceHostSet() map[string]struct{} {
	hosts := app.extraDeviceHosts()
	desired := make(map[string]struct{}, len(hosts))

	for _, host := range hosts {
		desired[host] = struct{}{}
	}

	return desired
}

func (app *WebApp) extraDeviceHostsPresent(desired map[string]struct{}) bool {
	for host := range desired {
		if _, ok := app.GetDevice(host); !ok {
			return false
		}
	}

	return true
}

func retryUntilReady(ctx context.Context, retryInterval time.Duration, attempt func() bool) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if attempt() {
			return
		}

		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// DiscoverDevices refreshes the device registry. When TriggerDiscovery is set
// (embedded build), it runs the host service's discovery so the shared store is
// refreshed, then re-syncs from ExtraDeviceHosts — it does NOT run its own
// mDNS/UPnP, so the embedded build never duplicates the service's discovery.
// When discoveryService is non-nil (standalone soundtouch-player), it runs an
// mDNS/UPnP sweep and registers any found devices via AddDeviceByHost.
// Used by the startup goroutine in main and by the /api/control/discover route
// inside MountWeb.
func (app *WebApp) DiscoverDevices(ctx context.Context, discoveryService *discovery.UnifiedDiscoveryService) {
	// External discovery (embedded build): run the host service's sweep so the
	// shared datastore is refreshed.
	if app.TriggerDiscovery != nil {
		app.TriggerDiscovery(ctx)
	}

	// Re-sync from the external device source (embedded: the service datastore).
	// No-op when ExtraDeviceHosts is unset.
	app.SeedExtraDevices()

	// Own mDNS/UPnP sweep — standalone only. The embedded build passes a nil
	// discovery service and relies entirely on the host service's discovery.
	if discoveryService == nil {
		return
	}

	log.Println("Starting device discovery...")

	devices, err := discoveryService.DiscoverDevices(ctx)
	if err != nil {
		log.Printf("Discovery failed: %v", err)
		app.BroadcastDiscoveryStatus("failed", app.DeviceCount())

		return
	}

	log.Printf("Found %d devices", len(devices))

	for _, device := range devices {
		app.AddDeviceByHost(device.Host, device.Port, classifySource(device.DiscoveryMethod))
	}
}

// classifySource labels a discovered device "manual" if it came from (at
// least in part) a configured host, "discovered" otherwise. discoveryMethod
// can be a "+"-joined composite (e.g. "Configuration+mDNS/Bonjour") when
// mergeDeviceData combines a configured host with the same device found via
// mDNS/UPnP in the same sweep -- match by substring, not exact equality, so
// a manually configured host that's also independently discoverable still
// gets labeled "manual".
func classifySource(discoveryMethod string) string {
	if strings.Contains(discoveryMethod, "Configuration") {
		return "manual"
	}

	return "discovered"
}
