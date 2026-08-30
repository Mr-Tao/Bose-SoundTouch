package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/discovery"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/amazon"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
	"github.com/gesellix/bose-soundtouch/pkg/service/health"
	"github.com/gesellix/bose-soundtouch/pkg/service/logbuf"
	"github.com/gesellix/bose-soundtouch/pkg/service/marge"
	"github.com/gesellix/bose-soundtouch/pkg/service/proxy"
	"github.com/gesellix/bose-soundtouch/pkg/service/setup"
	"github.com/gesellix/bose-soundtouch/pkg/service/spotify"
	"github.com/gesellix/bose-soundtouch/pkg/service/tts"
	"github.com/gesellix/bose-soundtouch/pkg/service/updatecheck"
	"github.com/gesellix/bose-soundtouch/pkg/ssh"
	"github.com/miekg/dns"
	"golang.org/x/sync/singleflight"
)

// spotifySourceMutex is a zero-value mutex with an optional test observer.
// The observer is installed before concurrent work starts and is nil in
// production.
type spotifySourceMutex struct {
	sync.Mutex
	sequence atomic.Uint64
	observe  func(call uint64, acquired bool)
}

func (m *spotifySourceMutex) Lock() {
	var call uint64

	observer := m.observe
	if observer != nil {
		call = m.sequence.Add(1)
		observer(call, false)
	}

	m.Mutex.Lock()
	if observer != nil {
		observer(call, true)
	}
}

// Server handles HTTP requests for the SoundTouch service.
type Server struct {
	ds                         *datastore.DataStore
	sm                         *setup.Manager
	mu                         sync.RWMutex
	serverURL                  string
	httpsServerURL             string // effective (derived or overridden) HTTPS URL
	httpsOverride              string // explicit HTTPS URL override; "" means derive from serverURL
	httpsPort                  string // configured HTTPS port, used when deriving
	httpsDefaultURL            string // startup hostname-based fallback when serverURL has no host
	httpsListenAddr            string
	discovering                bool
	redactLogs                 bool
	logBodies                  bool
	recordEnabled              bool
	discoveryInterval          time.Duration
	discoveryEnabled           bool
	updateCheckInterval        time.Duration // live update-check interval; see SetUpdateCheckSettings
	updateCheckEnabled         bool          // live update-check opt-in; defaults off (#591)
	dnsEnabled                 bool
	dnsUpstream                []string
	dnsBindAddr                string
	internalPaths              []string
	shortcuts                  map[string]int
	recorder                   *proxy.Recorder
	dnsDiscovery               *discovery.DNSDiscovery
	authProbes                 *authProbeRegistry
	authProbeTimeoutOverride   time.Duration // zero means use defaultAuthProbeTimeout; injectable for tests
	deprecatedRoutes           *deprecatedRouteTracker
	devicesChangedHook         func()
	Version                    string
	Commit                     string
	Date                       string
	RepoURL                    string
	mgmtUsername               string
	mgmtPassword               string
	adminAreaAuth              string               // "" (unset) / "enabled" / "disabled" — see datastore.Settings.AdminAreaAuth
	dismissedAnnouncements     map[string]time.Time // announcement id -> most recent dismissal; see RecordDismissal
	updateChecker              *updatecheck.Checker // the HTTP-checking object; nil unless SetUpdateChecker was called
	spotifyClientID            string
	spotifyClientSecret        string
	spotifyRedirectURI         string
	spotifyService             *spotify.Service
	spotifyOAuthMu             sync.Mutex
	spotifyOAuthTransactions   map[string]spotifyOAuthTransaction
	spotifyOAuthGenerations    map[string]uint64
	spotifyOAuthTTL            time.Duration
	spotifyOAuthRandom         io.Reader
	spotifyPrimes              singleflight.Group
	spotifySourceMu            spotifySourceMutex
	spotifyOAuthAfterStore     func() // test-only barrier at the commit/publication boundary
	spotifyPowerOnMu           sync.Mutex
	spotifyPrimeReadbackDelays []time.Duration
	spotifyPowerOnPrimer       func(string)
	amazonClientID             string
	amazonClientSecret         string
	amazonRedirectURI          string
	amazonService              *amazon.Service
	ttsService                 *tts.Service
	ttsProvider                string
	ttsGoogleAPIKey            string
	ttsGoogleEndpoint          string // test-only override; not exposed in the UI
	ttsAppKey                  string
	ttsLanguage                string
	ttsVoice                   string
	ttsVolume                  int
	peerObserver               *peerObserver
	healthRegistry             *health.Registry
	logBuf                     *logbuf.Buffer
	expectedHosts              []string
	ownCACache                 struct {
		once sync.Once
		cert *x509.Certificate
	}
}

// RequestSnapshot represents an immutable snapshot of an HTTP request.
type RequestSnapshot struct {
	Method    string
	URL       *url.URL
	Headers   http.Header
	Body      []byte
	Host      string
	Timestamp time.Time
}

type ctxKey struct{ name string }

// SnapshotKey is the context key for the RequestSnapshot.
var SnapshotKey = &ctxKey{"request_snapshot"}

var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// NormalizeServerURL trims surrounding whitespace and any trailing slashes from
// a configured server URL. A trailing slash poisons every URL built by string
// concatenation from it, most visibly the BMX registry base ("{BMX_SERVER}/bmx/
// tunein" in bmx_services.json): it would otherwise hand a speaker
// "http://host:8000//bmx/tunein" and make it request "//bmx/tunein/...", a path
// the chi router does not match, so playback 404s. It also keeps {MEDIA_SERVER}
// and the OAuth redirect URIs free of a stray double slash.
func NormalizeServerURL(serverURL string) string {
	return strings.TrimRight(strings.TrimSpace(serverURL), "/")
}

// NewServer creates a new SoundTouch service server.
func NewServer(ds *datastore.DataStore, sm *setup.Manager, serverURL string, redactLogs, logBodies, recordEnabled bool) *Server {
	s := &Server{
		ds:                ds,
		sm:                sm,
		serverURL:         NormalizeServerURL(serverURL),
		redactLogs:        redactLogs,
		logBodies:         logBodies,
		recordEnabled:     recordEnabled,
		discoveryInterval: 5 * time.Minute,
		discoveryEnabled:  true,
		// The update check is opt-in (#591): only the interval gets a default,
		// updateCheckEnabled stays false so no install starts making outbound
		// GitHub calls without an explicit yes.
		updateCheckInterval:        24 * time.Hour,
		peerObserver:               newPeerObserver(),
		healthRegistry:             health.NewRegistry(),
		authProbes:                 newAuthProbeRegistry(defaultAuthProbeTTL),
		deprecatedRoutes:           newDeprecatedRouteTracker(),
		spotifyOAuthTransactions:   make(map[string]spotifyOAuthTransaction),
		spotifyOAuthGenerations:    make(map[string]uint64),
		spotifyOAuthTTL:            10 * time.Minute,
		spotifyOAuthRandom:         rand.Reader,
		spotifyPrimeReadbackDelays: []time.Duration{250 * time.Millisecond, time.Second, 2 * time.Second},
	}
	s.spotifyPowerOnPrimer = func(host string) {
		go s.PrimeDeviceWithSpotify(host)
	}

	health.RegisterSourcesXMLPresent(s.healthRegistry, ds)
	health.RegisterSpeakerInfoReachable(s.healthRegistry, ds)
	health.RegisterSourcesXMLDiff(s.healthRegistry, ds)
	health.RegisterSpeakerMargeURLCheck(s.healthRegistry, ds, s.ExpectedHosts)
	health.RegisterRuntimeBmxURLStaleCheck(
		s.healthRegistry,
		ds,
		s.readSpeakerBmxRegistryURL,
		func() bool {
			running, _ := s.GetDNSRunning()
			return running
		},
	)
	health.RegisterCertChainCheck(
		s.healthRegistry,
		func() string {
			_, httpsURL := s.GetSettings()
			return httpsURL
		},
		s.actualHTTPSPort,
		s.loadOwnCACert,
	)
	health.RegisterCACertExpiryCheck(s.healthRegistry, s.loadOwnCACert, s.ownCACertPath)
	health.RegisterTestPlaybackCheck(s.healthRegistry, ds, func() string {
		serverURL, _ := s.GetSettings()
		return serverURL
	})
	health.RegisterOrionPathsCheck(s.healthRegistry, ds)
	health.RegisterPresetsCountCheck(s.healthRegistry, ds)
	health.RegisterPresetsConsistencyCheck(s.healthRegistry, ds)
	health.RegisterRefreshSourcesCheck(s.healthRegistry, ds)
	health.RegisterStaleInternetRadioCheck(s.healthRegistry, ds)
	health.RegisterDefaultAccountNonBoseDevicesCheck(s.healthRegistry, ds)
	health.RegisterSpeakerCABundleCheck(s.healthRegistry, ds, func(deviceIP string) (string, string, bool) {
		return s.sm.ProbeCABundles(deviceIP)
	})
	health.RegisterSpeakerClockCheck(s.healthRegistry, ds, func(ip string) (int64, int64, bool) {
		cfg := client.DefaultConfig()
		cfg.Host = ip
		cfg.Timeout = 5 * time.Second

		c := client.NewClient(cfg)
		ct, err := c.GetClockTime()

		if err != nil || ct == nil || ct.GetUTC() == 0 {
			return 0, 0, false
		}

		return ct.GetUTC(), ct.GetUTCSyncTime(), true
	}, s.setSpeakerClock)
	health.RegisterServerURLReachableCheck(s.healthRegistry, func() string {
		serverURL, _ := s.GetSettings()
		return serverURL
	})
	health.RegisterOAuthTargetReachableCheck(
		s.healthRegistry,
		func() string {
			serverURL, _ := s.GetSettings()
			return serverURL
		},
		s.GetDNSRunning,
	)
	health.RegisterSpotifyAccountLinkedCheck(
		s.healthRegistry,
		func() bool { return s.spotifyService != nil },
		func() int {
			if s.spotifyService == nil {
				return 0
			}

			return len(s.spotifyService.GetAccounts())
		},
	)
	health.RegisterMgmtDefaultCredentialsCheck(
		s.healthRegistry,
		func() (string, string) { return s.mgmtUsername, s.mgmtPassword },
	)
	health.RegisterAdminAreaAuthCheck(s.healthRegistry, s.AdminAreaAuthMode)

	// Health QuickFix executor for the empty-margeAccountUUID
	// finding from RegisterSpeakerInfoReachable. Lives here (not in
	// the health package) because the executor needs setup.Manager
	// to drive PairAccount — and the health package deliberately
	// avoids importing setup to keep its transitive dep surface
	// small (see the boundary comment near speakerInfoXML).
	s.healthRegistry.RegisterFix(
		health.CheckIDSpeakerInfoReachable,
		health.FixIDCompleteSpeakerPairing,
		s.completeSpeakerPairingFix,
	)

	// QuickFix executor for the speaker_marge_url mismatch finding.
	// Adds the speaker's actual margeURL host to settings.TLSExtraHosts
	// so a subsequent restart picks it up via applyPersistedSettings.
	s.healthRegistry.RegisterFix(
		health.CheckIDSpeakerMargeURL,
		health.FixIDAddMargeHostToTLS,
		s.addMargeHostToTLSFix,
	)

	// QuickFix executors for the speaker_ca_bundle integrity check.
	// Fix executors live here (not in the health package) because they
	// need setup.Manager — the same boundary as completeSpeakerPairingFix.
	s.healthRegistry.RegisterFix(
		health.CheckIDSpeakerCABundle,
		health.FixIDRestoreAndInjectCA,
		s.restoreAndInjectCAFix,
	)
	s.healthRegistry.RegisterFix(
		health.CheckIDSpeakerCABundle,
		health.FixIDInjectCACert,
		s.injectCACertFix,
	)
	health.RegisterDNSSanityCheck(
		s.healthRegistry,
		s.GetDNSRunning,
		func() string {
			serverURL, _ := s.GetSettings()

			ip, err := s.ResolveServerURLIPForPreflight(serverURL)
			if err != nil {
				return ""
			}

			return ip
		},
	)
	health.RegisterDNSSpeakerUsageCheck(
		s.healthRegistry,
		s.ds,
		s.GetDNSRunning,
		func() map[string]time.Time {
			if s.dnsDiscovery == nil {
				return map[string]time.Time{}
			}

			return s.dnsDiscovery.InterceptClientIPs()
		},
	)

	// QuickFix executor for the dns_speaker_usage per-device info findings.
	// Lives here (not in the health package) because it needs runDNSPathProbe,
	// which is part of the handlers layer. The health package deliberately
	// avoids importing handlers to keep its transitive dep surface small.
	//
	// Registered without refresh: this probe is a diagnostic whose value is the
	// result message ("DNS path OK" / "no callback ..."). A refresh would re-fetch
	// the whole health list and wipe that message from the UI before the operator
	// can read it. The operator can refresh manually to see a now-confirmed
	// speaker drop its finding.
	s.healthRegistry.RegisterFixNoRefresh(
		health.CheckIDDNSSpeakerUsage,
		"probe_dns_path",
		func(target health.Target) (string, error) {
			res, err := s.runDNSPathProbe(target.Device, "")
			if err != nil {
				return "", err
			}

			if res.Success {
				return fmt.Sprintf(
					"Speaker resolved a Bose hostname through AfterTouch in %.0fms. DNS path OK.",
					res.LatencyMs,
				), nil
			}

			msg := "No /v1/auth callback within the timeout; this speaker likely resolves Bose hostnames via a different DNS resolver."
			if res.Remediation != "" {
				msg += " " + res.Remediation
			}

			return msg, nil
		},
	)

	s.dismissedAnnouncements = loadDismissedAnnouncements(ds)

	return s
}

// clockSetTolerance is how close the speaker's clock must be to the target
// after a set for the set to count as successful.
const clockSetTolerance = 2 * time.Minute

// setSpeakerClock is the set_clock QuickFix executor. It sets the speaker's
// clock to the service's current time and verifies the clock actually moved
// before reporting success.
//
// Two transports are tried because firmware varies: some builds honour
// POST /clockTime, but others dispatch that POST to their read handler
// (HandleClockGetTime) and silently ignore it, so the HTTP path is a no-op
// there. SSH `date` reliably sets the system clock on an SSH-reachable
// speaker (root, empty password — the usual unlocked state). We verify by
// re-reading /clockTime regardless of which path "succeeded", so we never
// report success when the clock didn't change.
func (s *Server) setSpeakerClock(ip string) error {
	now := time.Now()

	// 1) HTTP /clockTime: harmless, and works on firmware that honours it.
	httpErr := s.setSpeakerClockHTTP(ip, now)
	if speakerClockWithin(ip, now, clockSetTolerance) {
		return nil
	}

	// 2) SSH `date`: the reliable path on firmware that ignores the HTTP POST.
	sshErr := setSpeakerClockSSH(ip, now)
	if speakerClockWithin(ip, now, clockSetTolerance) {
		return nil
	}

	return fmt.Errorf(
		"speaker clock unchanged after both transports (http: %v; ssh: %v). "+
			"This firmware ignores POST /clockTime (it handles the POST as a read), and SSH was not usable. "+
			"The durable fix is time sync: the speaker likely cannot resolve/reach an NTP server, "+
			"so restore DNS/NTP reachability (a wrong clock breaks HTTPS/TLS)",
		httpErr, sshErr,
	)
}

// setSpeakerClockHTTP pushes the time via POST /clockTime. Returns the
// request error (a 200 here does not guarantee the clock changed; the caller
// verifies separately).
func (s *Server) setSpeakerClockHTTP(ip string, t time.Time) error {
	cfg := client.DefaultConfig()
	cfg.Host = ip
	cfg.Timeout = 5 * time.Second

	return client.NewClient(cfg).SetClockTime(models.NewClockTimeRequest(t))
}

// setSpeakerClockSSH sets the speaker's system clock over SSH. The command is
// built only from the service's own timestamp (no user input). It tries the
// coreutils `-s` form first and falls back to the BusyBox positional form
// (MMDDhhmmCCYY.ss), covering both firmware flavours.
func setSpeakerClockSSH(ip string, t time.Time) error {
	utc := t.UTC()
	cmd := fmt.Sprintf(
		"date -u -s '%s' || date -u %s",
		utc.Format("2006-01-02 15:04:05"),
		utc.Format("010215042006.05"),
	)

	out, err := ssh.NewClient(ip).Run(cmd)
	if err != nil {
		return fmt.Errorf("ssh date: %w (%s)", err, strings.TrimSpace(out))
	}

	return nil
}

// speakerClockWithin reports whether the speaker's current clock is within
// tol of target. Used to verify a set actually took effect.
func speakerClockWithin(ip string, target time.Time, tol time.Duration) bool {
	cfg := client.DefaultConfig()
	cfg.Host = ip
	cfg.Timeout = 5 * time.Second

	ct, err := client.NewClient(cfg).GetClockTime()
	if err != nil || ct == nil {
		return false
	}

	utc := ct.GetUTC()
	if utc == 0 {
		return false
	}

	skew := target.Unix() - utc
	if skew < 0 {
		skew = -skew
	}

	return skew <= int64(tol.Seconds())
}

// SetExpectedHosts records the hostnames the service considers its
// own (serverURL host + httpsServerURL host + --tls-extra-host
// values). The Health tab's Marge-URL check reads this list at
// run time to decide whether a speaker's <margeURL> points at us.
func (s *Server) SetExpectedHosts(hosts []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, len(hosts))
	copy(out, hosts)

	s.expectedHosts = out
}

// ExpectedHosts returns a copy of the recorded expected-hosts list.
func (s *Server) ExpectedHosts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, len(s.expectedHosts))
	copy(out, s.expectedHosts)

	return out
}

// persistedTLSExtraHosts returns the slice of TLS extra hosts that
// live in settings.json. Used by HandleGetSettings to render the
// "edit list" UI separately from the full effective SAN list
// (ExpectedHosts also contains serverURL host, httpsServerURL host,
// hostname, and CLI/env-pinned extras). Returns an empty slice if
// the settings file is missing or unreadable — the caller should
// treat that the same as "operator hasn't added anything yet".
func (s *Server) persistedTLSExtraHosts() []string {
	persisted, err := s.ds.GetSettings()
	if err != nil {
		return []string{}
	}

	out := make([]string, len(persisted.TLSExtraHosts))
	copy(out, persisted.TLSExtraHosts)

	return out
}

// ownCACertPath returns the on-disk path of AfterTouch's own CA
// cert (PEM). Empty string when the certmanager isn't wired in.
// Used by the Health-tab CA-expiry check to render an accurate
// remediation command pointing at the actual file.
func (s *Server) ownCACertPath() string {
	if s.sm == nil || s.sm.Crypto == nil {
		return ""
	}

	return s.sm.Crypto.GetCACertPath()
}

// loadOwnCACert parses AfterTouch's own CA leaf from disk. Used
// by the Health-tab cert-chain check to definitively classify
// whether the HTTPS endpoint is serving a cert issued by this
// service's built-in CA (as opposed to a public CA or a foreign
// chain from a reverse proxy). Returns nil when the CA isn't
// configured or fails to parse — the caller falls back to a
// Subject==Issuer heuristic in that case.
//
// The parse is cached in ownCACache so repeated Health polls
// don't re-read the PEM. Restart-based config changes are
// picked up because Server itself is reconstructed.
func (s *Server) loadOwnCACert() *x509.Certificate {
	s.ownCACache.once.Do(func() {
		if s.sm == nil || s.sm.Crypto == nil {
			return
		}

		path := s.sm.Crypto.GetCACertPath()
		if path == "" {
			return
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return
		}

		block, _ := pem.Decode(data)
		if block == nil {
			return
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return
		}

		s.ownCACache.cert = cert
	})

	return s.ownCACache.cert
}

// ClientIPMiddleware returns a chi middleware that resolves the client IP into
// the request context (read via middleware.GetClientIP). Always returns a
// non-nil middleware: at minimum, the socket peer is recorded.
//
// When Settings.TrustForwardedHeaders is true and the immediate TCP peer is in
// the configured trusted-proxy list, the X-Forwarded-For header is also
// consulted: chi walks the chain right-to-left, skipping entries that fall
// within the trusted CIDRs, and stores the first untrusted entry as the client.
//
// The trusted-peer gate prevents the typical X-Forwarded-* spoofing surface:
// on a flat LAN where a malicious speaker could send the headers itself, we
// won't honour them; behind a documented reverse proxy on loopback we will.
func (s *Server) ClientIPMiddleware() func(http.Handler) http.Handler {
	settings, err := s.ds.GetSettings()
	if err != nil {
		log.Printf("[ClientIP] failed to load settings: %v - falling back to peer-only", err)
		return clientIPMiddleware(false, nil, nil)
	}

	return buildClientIPMiddleware(settings.TrustForwardedHeaders, settings.TrustedProxyCIDRs)
}

// SetVersionInfo sets the version information for the server.
func (s *Server) SetVersionInfo(version, commit, date, repoURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Version = version
	s.Commit = commit
	s.Date = date
	s.RepoURL = repoURL
}

// SetLogBuffer attaches a logbuf.Buffer to the server. When set,
// HandleGetLogs returns its contents; when nil, the endpoint
// reports an empty snapshot. Optional so that tests and
// alternative composers (the standalone web binary, etc.) don't
// have to construct a buffer they don't need.
func (s *Server) SetLogBuffer(buf *logbuf.Buffer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logBuf = buf
}

// LogBuffer returns the attached log buffer, or nil if none.
func (s *Server) LogBuffer() *logbuf.Buffer {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.logBuf
}

// SetDiscoverySettings sets the discovery settings for the server.
func (s *Server) SetDiscoverySettings(interval time.Duration, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.discoveryInterval = interval
	s.discoveryEnabled = enabled
}

// SetUpdateCheckSettings sets the live update-check settings for the server.
//
// Kept adjacent to its getter (rather than next to GetDiscoverySettings
// further down) so the pair reads as one unit; the background goroutine in
// soundtouch-service re-reads them on every poll, which is what makes the
// Settings-page toggle take effect without a restart.
func (s *Server) SetUpdateCheckSettings(interval time.Duration, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.updateCheckInterval = interval
	s.updateCheckEnabled = enabled
}

// GetUpdateCheckSettings returns the current update-check interval and enabled state.
func (s *Server) GetUpdateCheckSettings() (time.Duration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.updateCheckInterval, s.updateCheckEnabled
}

// SetDevicesChangedHook registers a callback fired after the known device set
// changes (a discovery sweep or a manual add). The embedded web UI uses it to
// re-sync its registry from the shared datastore — the single source of truth —
// so it never runs its own discovery. Nil-safe.
func (s *Server) SetDevicesChangedHook(hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.devicesChangedHook = hook
}

// notifyDevicesChanged fires the devices-changed hook, if one is registered.
func (s *Server) notifyDevicesChanged() {
	s.mu.RLock()
	hook := s.devicesChangedHook
	s.mu.RUnlock()

	if hook != nil {
		hook()
	}
}

// parseUpstreamDNS splits a comma-separated string of DNS servers.
func parseUpstreamDNS(upstream string) []string {
	var upstreamList []string

	if upstream != "" {
		for _, u := range strings.Split(upstream, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				upstreamList = append(upstreamList, u)
			}
		}
	}

	return upstreamList
}

// getSystemDNS returns the DNS servers from /etc/resolv.conf.
func getSystemDNS() []string {
	config, _ := dns.ClientConfigFromFile("/etc/resolv.conf")
	if config != nil && len(config.Servers) > 0 {
		return config.Servers
	}

	return nil
}

// areUpstreamsEqual compares two slices of DNS server addresses.
func areUpstreamsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// SetDNSSettings sets the DNS discovery settings for the server.
func (s *Server) SetDNSSettings(enabled bool, upstream, bind string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldBind := s.dnsBindAddr
	oldUpstream := s.dnsUpstream

	s.dnsEnabled = enabled
	s.dnsBindAddr = bind

	upstreamList := parseUpstreamDNS(upstream)

	// Try to get system DNS if none provided
	if enabled && len(upstreamList) == 0 {
		upstreamList = getSystemDNS()
		if len(upstreamList) > 0 {
			log.Printf("[DNS] Using system DNS servers from /etc/resolv.conf: %v", upstreamList)
		}
	}

	s.dnsUpstream = upstreamList
	upstreamChanged := !areUpstreamsEqual(upstreamList, oldUpstream)

	if s.dnsDiscovery != nil {
		if !enabled || bind != oldBind || upstreamChanged {
			log.Printf("[DNS] Settings changed, stopping DNS discovery server")

			_ = s.dnsDiscovery.Shutdown()
			s.dnsDiscovery = nil
		}
	}

	if enabled && len(upstreamList) == 0 {
		log.Printf("[DNS] Cannot start DNS discovery server: upstream DNS is empty and no system DNS found")

		s.dnsEnabled = false

		return
	}

	if enabled && s.dnsDiscovery == nil {
		s.startDNSDiscovery(bind, upstreamList)
	}
}

// ResolveServerURLIPForPreflight is an exported wrapper around resolveServerURLIP
// so callers outside the package (e.g. the service startup pre-flight) can
// reuse the same resolution path the DNS server uses.
func (s *Server) ResolveServerURLIPForPreflight(serverURL string) (string, error) {
	return s.resolveServerURLIP(serverURL)
}

// resolveServerURLIP returns the IP that the DNS server would hand out as the
// intercept answer for the given server URL. An empty URL, empty hostname, or a
// hostname that cannot be resolved to an IP is reported as an error so callers
// can refuse to start (or reject user input) instead of silently degrading.
// "localhost" is treated as 127.0.0.1.
func (s *Server) resolveServerURLIP(serverURL string) (string, error) {
	if strings.TrimSpace(serverURL) == "" {
		return "", fmt.Errorf("server URL is empty")
	}

	u, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("invalid server URL %q: %w", serverURL, err)
	}

	hostname := u.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("server URL %q has no hostname", serverURL)
	}

	if hostname == "localhost" {
		return "127.0.0.1", nil
	}

	if ip := net.ParseIP(hostname); ip != nil {
		return ip.String(), nil
	}

	// Prefer the setup manager's resolver (it cascades through device SSH ping
	// then system DNS). Fall back to plain system DNS when no manager is wired,
	// so this works in tests and lightweight server constructions.
	if s.sm != nil {
		if resolved := s.sm.GetResolvedIP(hostname); net.ParseIP(resolved) != nil {
			return resolved, nil
		}
	} else if ips, lookupErr := net.LookupIP(hostname); lookupErr == nil {
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil {
				return v4.String(), nil
			}
		}

		if len(ips) > 0 {
			return ips[0].String(), nil
		}
	}

	return "", fmt.Errorf("hostname %q did not resolve to an IP — "+
		"set the server URL to an IP, or to a hostname this host can resolve",
		hostname)
}

func (s *Server) startDNSDiscovery(bind string, upstreamList []string) {
	log.Printf("[DNS] Starting DNS discovery server on %s", sanitizeLog(bind))

	serviceIP, err := s.resolveServerURLIP(s.serverURL)
	if err != nil {
		log.Printf("[DNS] Cannot start DNS discovery server: %s", sanitizeErr(err))

		s.dnsEnabled = false

		return
	}

	s.dnsDiscovery = discovery.NewDNSDiscovery(upstreamList, serviceIP, s.serverURL)
	go func(d *discovery.DNSDiscovery, addr string) {
		if err := d.Start(addr); err != nil {
			log.Printf("Warning: DNS discovery server error: %v", err)
		}
	}(s.dnsDiscovery, bind)
}

// GetDNSRunning returns whether DNS discovery is active and its bind address.
func (s *Server) GetDNSRunning() (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.dnsDiscovery == nil {
		return false, ""
	}

	return s.dnsDiscovery.IsRunning(s.dnsBindAddr), s.dnsBindAddr
}

// SetDNSDiscoveries sets the initial DNS discoveries for the server.
func (s *Server) SetDNSDiscoveries(discoveries map[string]*discovery.DiscoveredHost) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dnsDiscovery != nil {
		s.dnsDiscovery.SetDiscovered(discoveries)
	}
}

// GetDNSDiscovery returns the current DNS discoveries.
func (s *Server) GetDNSDiscovery() map[string]*discovery.DiscoveredHost {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.dnsDiscovery == nil {
		return nil
	}

	return s.dnsDiscovery.GetDiscovered()
}

// SetShortcuts sets the request shortcuts for the server.
func (s *Server) SetShortcuts(shortcuts map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.shortcuts = shortcuts
}

// GetShortcuts returns the current request shortcuts.
func (s *Server) GetShortcuts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.shortcuts
}

// GetDiscoverySettings returns the current discovery settings.
func (s *Server) GetDiscoverySettings() (time.Duration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.discoveryInterval, s.discoveryEnabled
}

// SetHTTPServerURL sets the external HTTPS URL of the service.
//
// Deprecated: prefer SetHTTPSSettings, which tracks the override vs the
// derived value so the effective URL follows the Target Domain. Kept for
// callers that set the effective URL directly.
func (s *Server) SetHTTPServerURL(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.httpsServerURL = url
}

// SetHTTPSSettings records the HTTPS URL override (empty = derive from
// the Target Domain), the configured HTTPS port, and the startup
// hostname-based fallback, then recomputes the effective HTTPS URL.
func (s *Server) SetHTTPSSettings(override, httpsPort, defaultURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.httpsOverride = strings.TrimSpace(override)
	s.httpsPort = httpsPort
	s.httpsDefaultURL = defaultURL
	s.recomputeHTTPSURLLocked()
}

// recomputeHTTPSURLLocked refreshes the effective HTTPS URL from the
// current serverURL + override + port. Callers must hold s.mu.
func (s *Server) recomputeHTTPSURLLocked() {
	s.httpsServerURL = DeriveHTTPSURL(s.serverURL, s.httpsOverride, s.httpsPort, s.httpsDefaultURL)
}

// applyHTTPSOverrideLocked sets the HTTPS override from an optional
// request value (nil = preserve the current one, non-nil replaces it,
// empty re-enables deriving) and recomputes the effective URL. Callers
// must hold s.mu.
func (s *Server) applyHTTPSOverrideLocked(override *string) {
	if override != nil {
		s.httpsOverride = strings.TrimSpace(*override)
	}

	s.recomputeHTTPSURLLocked()
}

// HTTPSOverride returns the explicit HTTPS URL override, or "" when the
// effective URL is derived from the Target Domain.
func (s *Server) HTTPSOverride() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.httpsOverride
}

// SetHTTPSListenAddr records the address the HTTPS listener is bound
// to (e.g. ":8443"). The cert-chain health check uses its port to
// detect an advertised-URL/listener port mismatch (issue #355).
func (s *Server) SetHTTPSListenAddr(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.httpsListenAddr = addr
}

// actualHTTPSPort returns the port the HTTPS listener is bound to, or
// "" if unknown/unparseable.
func (s *Server) actualHTTPSPort() string {
	s.mu.RLock()
	addr := s.httpsListenAddr
	s.mu.RUnlock()

	if addr == "" {
		return ""
	}

	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}

	return port
}

// SetRecorder sets the recorder for the server.
func (s *Server) SetRecorder(r *proxy.Recorder) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recorder = r
	if r != nil {
		r.Redact = s.redactLogs
	}
}

// SetSpotifyConfig sets the Spotify OAuth configuration.
func (s *Server) SetSpotifyConfig(clientID, clientSecret, redirectURI string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.spotifyClientID = clientID
	s.spotifyClientSecret = clientSecret
	s.spotifyRedirectURI = redirectURI
}

// SetAmazonConfig sets the Amazon LWA OAuth configuration.
func (s *Server) SetAmazonConfig(clientID, clientSecret, redirectURI string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.amazonClientID = clientID
	s.amazonClientSecret = clientSecret
	s.amazonRedirectURI = redirectURI
}

// GetSpotifyConfig returns the current Spotify OAuth configuration.
func (s *Server) GetSpotifyConfig() (clientID, clientSecret, redirectURI string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.spotifyClientID, s.spotifyClientSecret, s.spotifyRedirectURI
}

// GetAmazonConfig returns the current Amazon LWA OAuth configuration.
func (s *Server) GetAmazonConfig() (clientID, clientSecret, redirectURI string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.amazonClientID, s.amazonClientSecret, s.amazonRedirectURI
}

// applyMusicServiceCredentials updates music service credential fields on the
// server. Must be called with s.mu held. Empty secrets and "***" mean
// unchanged. A present redirect-URI pointer, including an empty value, replaces
// the override so operators can return to the derived callback URL.
func (s *Server) applyMusicServiceCredentials(spotifyID, spotifySecret string, spotifyURI *string, amazonID, amazonSecret, amazonURI string) {
	if spotifyID != "" {
		s.spotifyClientID = spotifyID
	}

	if spotifySecret != "" && spotifySecret != "***" {
		s.spotifyClientSecret = spotifySecret
	}

	if spotifyURI != nil {
		s.spotifyRedirectURI = strings.TrimSpace(*spotifyURI)
	}

	if amazonID != "" {
		s.amazonClientID = amazonID
	}

	if amazonSecret != "" && amazonSecret != "***" {
		s.amazonClientSecret = amazonSecret
	}

	if amazonURI != "" {
		s.amazonRedirectURI = amazonURI
	}
}

// EffectiveSpotifyRedirectURI returns the exact callback URI used in Spotify
// authorization and token exchange requests.
func (s *Server) EffectiveSpotifyRedirectURI() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.spotifyRedirectURI != "" {
		return s.spotifyRedirectURI
	}

	return s.serverURL + "/mgmt/spotify/callback"
}

// ReinitSpotifyService applies the current config without replacing a live
// service. Keeping one service instance preserves its account-generation fence,
// so an operation started before reconfiguration cannot later overwrite the
// same accounts file through a detached instance.
func (s *Server) ReinitSpotifyService() {
	s.supersedeSpotifyOAuthTransactions()

	clientID, clientSecret, redirectURI := s.GetSpotifyConfig()
	if redirectURI == "" {
		redirectURI = s.EffectiveSpotifyRedirectURI()
	}

	if err := ValidateSpotifyAuthorizationConfig(clientID, clientSecret, redirectURI); err != nil {
		log.Printf("[Spotify] Service not reinitialized: %s", sanitizeErr(err))
		return
	}

	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()

	if svc != nil {
		svc.Reconfigure(clientID, clientSecret, redirectURI)
		log.Printf("[Spotify] Service reconfigured")

		return
	}

	svc = spotify.NewSpotifyService(clientID, clientSecret, redirectURI, s.ds.DataDir)
	if err := svc.Load(); err != nil {
		log.Printf("[Spotify] Failed to load accounts during reinit: %v", err)
	}

	s.SetSpotifyService(svc)
	log.Printf("[Spotify] Service reinitialized")
}

// ReinitAmazonService creates a new Amazon service from current config and replaces the running one.
func (s *Server) ReinitAmazonService() {
	clientID, clientSecret, redirectURI := s.GetAmazonConfig()
	if clientID == "" {
		return
	}

	if redirectURI == "" {
		redirectURI = s.serverURL + "/mgmt/amazon/callback"
	}

	svc := amazon.NewAmazonService(clientID, clientSecret, redirectURI, s.ds.DataDir)
	if err := svc.Load(); err != nil {
		log.Printf("[Amazon] Failed to load accounts during reinit: %v", err)
	}

	s.SetAmazonService(svc)
	log.Printf("[Amazon] Service reinitialized")
}

// SetMgmtConfig sets the management API authentication credentials.
func (s *Server) SetMgmtConfig(username, password string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.mgmtUsername = username
	s.mgmtPassword = password
}

// SetAdminAreaAuth sets the live admin-area auth mode ("" / "enabled" /
// "disabled", see datastore.Settings.AdminAreaAuth). Does not validate —
// callers (HandleUpdateSettings, startup settings application) are
// responsible for only passing already-validated values.
func (s *Server) SetAdminAreaAuth(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.adminAreaAuth = mode
}

// AdminAreaAuthMode returns the live admin-area auth mode. Exported so
// packages that can't import handlers directly (e.g. health checks, which
// take it via a callback to avoid a circular import) can read it.
func (s *Server) AdminAreaAuthMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.adminAreaAuth
}

// activityKindNotificationDismissed is the datastore.RecordActivity "kind"
// used for announcement-banner dismissals (see #419 design,
// _/i419/design-admin-area-auth-gate.md).
const activityKindNotificationDismissed = "notification_dismissed"

// loadDismissedAnnouncements scans the local activity log once at startup
// and folds it into an id -> most-recent-dismissal-timestamp map. Called
// from NewServer so the read path (IsAnnouncementDismissed) never touches
// disk — only this one, scoped, boot-time scan does, regardless of how
// large the log grows over time. Errors are logged, not fatal: a missing or
// unreadable activity log means "nothing dismissed yet", not a startup failure.
func loadDismissedAnnouncements(ds *datastore.DataStore) map[string]time.Time {
	dismissed := make(map[string]time.Time)

	if ds == nil {
		return dismissed
	}

	records, err := ds.GetActivityRecords(activityKindNotificationDismissed)
	if err != nil {
		log.Printf("[Announcements] Failed to load dismissal history, treating as none: %v", err)
		return dismissed
	}

	for _, record := range records {
		ts, parseErr := time.Parse(time.RFC3339Nano, record.Timestamp)
		if parseErr != nil {
			continue
		}

		if existing, ok := dismissed[record.ID]; !ok || ts.After(existing) {
			dismissed[record.ID] = ts
		}
	}

	return dismissed
}

// RecordDismissal marks an announcement as dismissed: appends to the local
// activity log (write-through) and updates the in-memory cache immediately,
// so IsAnnouncementDismissed reflects it without re-reading disk. The same
// id can be dismissed again later (e.g. if re-shown) — each call is a new
// log entry, not an overwrite.
func (s *Server) RecordDismissal(id string) error {
	if s.ds != nil {
		if err := s.ds.RecordActivity(activityKindNotificationDismissed, id, nil); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dismissedAnnouncements == nil {
		s.dismissedAnnouncements = make(map[string]time.Time)
	}

	s.dismissedAnnouncements[id] = time.Now()

	return nil
}

// IsAnnouncementDismissed reports whether the given announcement id has
// been dismissed, from the in-memory cache only — never touches disk.
func (s *Server) IsAnnouncementDismissed(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, ok := s.dismissedAnnouncements[id]

	return ok
}

// SetUpdateChecker registers the update checker (#591). The checker itself is
// always constructed and registered, regardless of whether the periodic check
// is enabled, so /api/setup/version and the Announcements banner can read
// LastResult() (e.g. a result persisted by an earlier run) even before the
// periodic check has ever run. Only the periodic background check is gated by
// the live enabled setting — see SetUpdateCheckSettings. Callers that leave
// this nil are still safe: UpdateCheckResult returns the zero value.
func (s *Server) SetUpdateChecker(c *updatecheck.Checker) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.updateChecker = c
}

// UpdateCheckResult returns the last known update-check result, or the
// zero value (Available: false) if the check was never enabled.
func (s *Server) UpdateCheckResult() updatecheck.Result {
	s.mu.RLock()
	checker := s.updateChecker
	s.mu.RUnlock()

	if checker == nil {
		return updatecheck.Result{}
	}

	return checker.LastResult()
}

// SetInternalPaths sets the internal paths for the server.
func (s *Server) SetInternalPaths(paths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.internalPaths = paths
}

// SetAmazonService sets the Amazon OAuth service.
func (s *Server) SetAmazonService(as *amazon.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.amazonService = as
}

// IsAmazonConfigured returns whether Amazon Music integration is configured.
func (s *Server) IsAmazonConfigured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.amazonService != nil
}

// SetSpotifyService sets the Spotify OAuth service.
func (s *Server) SetSpotifyService(ss *spotify.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.spotifyService = ss
}

// SetTTSService sets the text-to-speech service.
func (s *Server) SetTTSService(t *tts.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ttsService = t
}

// ttsSvc returns the configured TTS service, or nil if none is set.
func (s *Server) ttsSvc() *tts.Service {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.ttsService
}

// SetTTSConfig sets the text-to-speech configuration. An empty endpoint keeps
// the production default. Call ReinitTTSService afterwards to (re)build the
// running service from this config.
func (s *Server) SetTTSConfig(provider, googleAPIKey, googleEndpoint, appKey, language, voice string, volume int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ttsProvider = provider
	s.ttsGoogleAPIKey = googleAPIKey
	s.ttsGoogleEndpoint = googleEndpoint
	s.ttsAppKey = appKey
	s.ttsLanguage = language
	s.ttsVoice = voice
	s.ttsVolume = volume
}

// GetTTSConfig returns the current text-to-speech configuration.
func (s *Server) GetTTSConfig() (provider, googleAPIKey, appKey, language, voice string, volume int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.ttsProvider, s.ttsGoogleAPIKey, s.ttsAppKey, s.ttsLanguage, s.ttsVoice, s.ttsVolume
}

// applyTTSConfig updates TTS config from a settings save. Empty provider keeps
// the existing one; empty or "***" secrets (API key, app key) keep the existing
// value so the UI never has to round-trip them. Caller must hold s.mu.
func (s *Server) applyTTSConfig(provider, googleAPIKey, appKey, language, voice string, volume int) {
	if provider != "" {
		s.ttsProvider = provider
	}

	if googleAPIKey != "" && googleAPIKey != "***" {
		s.ttsGoogleAPIKey = googleAPIKey
	}

	if appKey != "" && appKey != "***" {
		s.ttsAppKey = appKey
	}

	s.ttsLanguage = language
	s.ttsVoice = voice
	s.ttsVolume = volume
}

// ttsConfigured reports whether TTS is usefully configured: an app_key is
// required to play on the speaker, and the google-cloud provider additionally
// needs an API key. Caller must hold at least s.mu.RLock.
func (s *Server) ttsConfigured() bool {
	if s.ttsAppKey == "" {
		return false
	}

	if s.ttsProvider == tts.ProviderGoogleCloud {
		return s.ttsGoogleAPIKey != ""
	}

	return true
}

// ReinitTTSService builds the TTS service from the current config and replaces
// the running one. Unlike the OAuth services it always installs a service (the
// translate provider needs no credentials); the provider is chosen by
// s.ttsProvider, defaulting to translate.
func (s *Server) ReinitTTSService() {
	s.mu.RLock()
	provider := s.ttsProvider
	apiKey := s.ttsGoogleAPIKey
	endpoint := s.ttsGoogleEndpoint
	cfg := tts.Config{
		BaseURL:         s.serverURL,
		AppKey:          s.ttsAppKey,
		DefaultLanguage: s.ttsLanguage,
		DefaultVoice:    s.ttsVoice,
		DefaultVolume:   s.ttsVolume,
	}
	s.mu.RUnlock()

	var p tts.Provider

	switch provider {
	case tts.ProviderGoogleCloud:
		if apiKey == "" {
			log.Printf("[TTS] Provider 'google-cloud' selected but no API key set; synthesis will fail until one is provided")
		}

		cloud := tts.NewCloudProvider(apiKey)
		if endpoint != "" {
			cloud.SetEndpoint(endpoint)
		}

		p = cloud
	case tts.ProviderTranslate, "":
		p = tts.NewTranslateProvider()
	default:
		log.Printf("[TTS] Unknown provider %q; falling back to 'translate'", provider)

		p = tts.NewTranslateProvider()
	}

	s.SetTTSService(tts.NewService(p, cfg))
	log.Printf("[TTS] Service reinitialized (provider: %s)", p.Name())
}

// GetRecordEnabled returns whether recording is enabled.
func (s *Server) GetRecordEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.recordEnabled
}

// GetSettings returns the current server settings.
func (s *Server) GetSettings() (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.serverURL, s.httpsServerURL
}

// IsSpotifyConfigured returns whether Spotify integration is configured.
func (s *Server) IsSpotifyConfigured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.spotifyService != nil
}

// GetLoggingSettings returns the current logging settings (redact / log-body / record).
func (s *Server) GetLoggingSettings() (bool, bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.redactLogs, s.logBodies, s.recordEnabled
}

// DiscoverDevices starts a background device discovery process.
//
//nolint:contextcheck
func (s *Server) DiscoverDevices(ctx context.Context) {
	s.discovering = true

	defer func() { s.discovering = false }()

	log.Println("Scanning for Bose devices...")

	// Use background context if none provided or if it's likely a request context
	if ctx == nil {
		ctx = context.Background()
	}

	// Always wrap in a timeout to prevent hanging forever
	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	svc := discovery.NewService(10 * time.Second)

	devices, err := svc.DiscoverDevices(discoveryCtx)
	if err != nil {
		log.Printf("Discovery error: %v", err)
		return
	}

	for _, d := range devices {
		s.handleDiscoveredDevice(*d)
	}

	// Post-discovery cleanup: merge overlapping IP/Serial entries
	s.mergeOverlappingDevices()

	// Let any observer (e.g. the embedded web UI) re-sync from the datastore.
	s.notifyDevicesChanged()
}

// findExistingDeviceInfoByDeviceID looks for existing device info by deviceID
func (s *Server) findExistingDeviceInfoByDeviceID(deviceID string) *models.ServiceDeviceInfo {
	allDevices, err := s.ds.ListAllDevices()
	if err != nil {
		return nil
	}

	for i := range allDevices {
		device := &allDevices[i]
		if device.DeviceID == deviceID {
			return device
		}
	}

	return nil
}

// SpotifyPrimeResult describes the verified result of one coalesced priming
// operation. An HTTP write alone is never reported as confirmed.
type SpotifyPrimeResult struct {
	DeviceIP       string `json:"device_ip"`
	DeviceID       string `json:"device_id,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	Outcome        string `json:"outcome"`
	WriteAttempted bool   `json:"write_attempted"`
	Detail         string `json:"detail,omitempty"`
}

type spotifyPrimeAttempt struct {
	accountID      string
	deviceID       string
	userID         string
	host           string
	port           string
	username       string
	accessToken    string
	linked         spotify.LinkedAccount
	info           spotify.ZeroConfInfo
	readbackDelays []time.Duration
}

// PrimeDeviceWithSpotify verifies or establishes one speaker's active Spotify
// identity. Concurrent requests for the same target share one operation.
func (s *Server) PrimeDeviceWithSpotify(deviceIP string) SpotifyPrimeResult {
	value, _, _ := s.spotifyPrimes.Do(deviceIP, func() (interface{}, error) {
		return s.primeDeviceWithSpotify(deviceIP), nil
	})

	result, ok := value.(SpotifyPrimeResult)
	if !ok {
		panic(fmt.Sprintf("unexpected Spotify prime result type %T", value))
	}

	return result
}

func (s *Server) primeDeviceWithSpotify(deviceIP string) SpotifyPrimeResult {
	result := SpotifyPrimeResult{DeviceIP: deviceIP, Outcome: "failed"}
	attempt, detail := s.prepareSpotifyPrime(deviceIP)
	result.DeviceID = attempt.deviceID
	result.UserID = attempt.userID

	if detail != "" {
		result.Detail = detail

		return result
	}

	log.Printf("[Spotify Watchdog] Priming %s with Spotify user %s", sanitizeLog(deviceIP), sanitizeLog(attempt.username))
	s.spotifySourceMu.Lock()
	if !s.spotifyPrimeOwnershipCurrent(attempt.accountID, attempt.deviceID, attempt.linked) {
		s.spotifySourceMu.Unlock()

		result.Detail = "Spotify source ownership changed before credential write"

		return result
	}

	result.WriteAttempted = true
	pushErr := s.pushSpotifyTokenToDevice(deviceIP, attempt.info, attempt.username, attempt.accessToken)
	s.spotifySourceMu.Unlock()

	writeNoOp := errors.Is(pushErr, spotify.ErrAddUserNoOp)
	if pushErr != nil && !writeNoOp {
		log.Printf("[Spotify Watchdog] Failed to prime %s: %s", sanitizeLog(deviceIP), sanitizeErr(pushErr))

		result.Detail = "ZeroConf addUser failed"

		return result
	}

	readbackStart := time.Now()
	for _, delay := range attempt.readbackDelays {
		if remaining := delay - time.Since(readbackStart); remaining > 0 {
			time.Sleep(remaining)
		}

		readback, readErr := spotify.ZeroConfFetchInfo(attempt.host, attempt.port)
		if readErr != nil {
			continue
		}

		if readback.ActiveUser == attempt.username {
			if refreshErr := s.refreshSpotifySourceAfterPrime(deviceIP, attempt.accountID, attempt.deviceID, attempt.linked); refreshErr != nil {
				result.Outcome = "unverified"
				result.Detail = "active user verified but source inventory publication failed"

				return result
			}

			if writeNoOp {
				result.Outcome = "unverified"
				result.Detail = "speaker retained the target user after an unconfirmed addUser no-op"

				return result
			}

			result.Outcome = "confirmed"
			result.Detail = "active user and source inventory verified after one credential write"

			return result
		}

		if readback.ActiveUser != "" {
			result.Outcome = "failed"
			result.Detail = "speaker switched to a different active Spotify user"

			return result
		}
	}

	result.Outcome = "unverified"
	if writeNoOp {
		result.Detail = "addUser returned an unconfirmed no-op and getInfo did not verify new credentials"
	} else {
		result.Detail = "credential write was not confirmed by getInfo"
	}

	return result
}

func (s *Server) prepareSpotifyPrime(deviceIP string) (spotifyPrimeAttempt, string) {
	attempt := spotifyPrimeAttempt{
		host: deviceIP,
		port: "8200",
	}

	s.mu.RLock()
	svc := s.spotifyService
	attempt.readbackDelays = append([]time.Duration(nil), s.spotifyPrimeReadbackDelays...)
	s.mu.RUnlock()

	if svc == nil {
		return attempt, "Spotify service not configured"
	}

	if parsedHost, parsedPort, splitErr := net.SplitHostPort(deviceIP); splitErr == nil {
		attempt.host = parsedHost
		attempt.port = parsedPort
	}

	attempt.accountID, attempt.deviceID = s.resolvePairedAccount(deviceIP, attempt.host)
	if attempt.accountID == "" || attempt.deviceID == "" {
		return attempt, "speaker has no exact Marge account binding"
	}

	sources, err := s.ds.GetConfiguredSources(attempt.accountID, attempt.deviceID)
	if err != nil {
		return attempt, "configured sources unavailable"
	}

	binding, err := bindingFromSources(attempt.accountID, attempt.deviceID, sources)
	if err != nil {
		return attempt, "Spotify source ownership unavailable"
	}

	linked, err := s.validateSpotifyBinding(binding, binding.Secret)
	if err != nil {
		return attempt, "Spotify source does not match a linked identity"
	}

	attempt.userID = linked.UserID

	attempt.accessToken, attempt.username, err = svc.GetFreshTokenForUser(linked.UserID)
	if err != nil {
		log.Printf("[Spotify Watchdog] Failed to get fresh token for %s: %v", sanitizeLog(deviceIP), err)

		return attempt, "Spotify token unavailable"
	}

	currentLinked, ok := svc.GetLinkedAccount(linked.UserID)
	if !ok || currentLinked.BoseSecret != linked.BoseSecret {
		return attempt, "Spotify identity changed while obtaining a token"
	}

	attempt.linked = currentLinked

	attempt.info, err = spotify.ZeroConfFetchInfo(attempt.host, attempt.port)
	if err != nil {
		return attempt, "ZeroConf getInfo failed"
	}

	if attempt.info.ActiveUser != "" && attempt.info.ActiveUser != attempt.username {
		return attempt, "speaker has a different active Spotify user"
	}

	return attempt, ""
}

// spotifyPrimeOwnershipCurrent must be called with spotifySourceMu held. The
// caller keeps that lock through its one bounded external credential write so
// a newer source publication cannot pass this check and then be overwritten.
func (s *Server) spotifyPrimeOwnershipCurrent(accountID, deviceID string, expected spotify.LinkedAccount) bool {
	sources, err := s.ds.GetConfiguredSources(accountID, deviceID)
	if err != nil {
		return false
	}

	binding, err := bindingFromSources(accountID, deviceID, sources)
	if err != nil || binding.UserID != expected.UserID || binding.Secret != expected.BoseSecret {
		return false
	}

	return s.spotifyLinkedAccountCurrent(expected)
}

func (s *Server) refreshSpotifySourceAfterPrime(deviceIP, accountID, deviceID string, account spotify.LinkedAccount) error {
	s.spotifySourceMu.Lock()
	if !s.spotifyLinkedAccountCurrent(account) {
		s.spotifySourceMu.Unlock()
		return fmt.Errorf("spotify identity changed before priming source publication")
	}

	configuredSources, err := s.ds.GetConfiguredSources(accountID, deviceID)
	if err != nil {
		s.spotifySourceMu.Unlock()
		return fmt.Errorf("read authoritative Spotify source: %w", err)
	}

	binding, err := bindingFromSources(accountID, deviceID, configuredSources)
	if err != nil || binding.UserID != account.UserID || binding.Secret != account.BoseSecret {
		s.spotifySourceMu.Unlock()
		return fmt.Errorf("spotify source ownership changed before priming publication")
	}

	if _, addErr := marge.AddSource(s.ds, accountID, account.UserID, strconv.Itoa(constants.SpotifyProviderID), account.BoseSecret, constants.CredentialTypeTokenV3, account.DisplayName); addErr != nil {
		s.spotifySourceMu.Unlock()
		log.Printf("[Spotify Watchdog] Failed to refresh Spotify source for account %s: %v", sanitizeLog(accountID), addErr)

		return addErr
	}

	if !s.spotifyLinkedAccountCurrent(account) {
		s.spotifySourceMu.Unlock()
		return fmt.Errorf("spotify identity changed during priming source publication")
	}
	s.spotifySourceMu.Unlock()

	cfg := client.DefaultConfig()
	cfg.Host = deviceIP
	cfg.Timeout = 5 * time.Second

	c := client.NewClient(cfg)
	if notifyErr := c.NotifySourcesUpdated(deviceID); notifyErr != nil {
		log.Printf("[Spotify Watchdog] sourcesUpdated notification for %s failed: %v", sanitizeLog(deviceIP), notifyErr)
		return notifyErr
	}

	sources, err := c.GetSources()
	if err != nil {
		log.Printf("[Spotify Watchdog] /sources readback for %s failed: %v", sanitizeLog(deviceIP), err)
		return err
	}

	found := false

	for _, source := range sources.GetReadySpotifySources() {
		if source.SourceAccount == account.UserID {
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("speaker source inventory does not contain a ready Spotify source for the linked identity")
	}

	// The provider exchange and configured-source writers can advance ownership
	// while the external notification/readback is in flight. Recheck under the
	// shared ownership lock before allowing the caller to report confirmation.
	s.spotifySourceMu.Lock()
	current := s.spotifyPrimeOwnershipCurrent(accountID, deviceID, account)
	s.spotifySourceMu.Unlock()

	if !current {
		return fmt.Errorf("spotify identity or source ownership changed during priming readback")
	}

	s.notifyDevicesChanged()

	return nil
}

// resolvePairedAccount returns the device's currently-paired account ID and its
// canonical deviceID. It prefers the live :8090/info margeAccountUUID (matches
// what the device will actually send on storePreset) and falls back to the
// datastore record. Mirrors setup.populateDeviceInfo's resolution order so
// priming and migration agree on which account a device belongs to.
//
// deviceIP is the original input (may carry a :port for tests); host is the
// bare host for datastore IPAddress matching.
func (s *Server) resolvePairedAccount(deviceIP, host string) (accountID, deviceID string) {
	if devInfo := s.findExistingDeviceInfoByIP(host); devInfo != nil {
		accountID = devInfo.AccountID
		deviceID = devInfo.DeviceID
	}

	if s.sm != nil {
		if info, err := s.sm.GetLiveDeviceInfo(deviceIP); err == nil {
			if info.MargeAccountUUID != "" {
				accountID = info.MargeAccountUUID
			}

			if info.DeviceID != "" {
				deviceID = info.DeviceID
			}
		} else {
			log.Printf("[Spotify Watchdog] live /info lookup for %s failed: %s (falling back to datastore account=%q)", sanitizeLog(deviceIP), sanitizeErr(err), sanitizeLog(accountID))
		}
	}

	return accountID, deviceID
}

// findExistingDeviceInfoByIP looks up a device record by IP address across all accounts.
func (s *Server) findExistingDeviceInfoByIP(ip string) *models.ServiceDeviceInfo {
	allDevices, err := s.ds.ListAllDevices()
	if err != nil {
		return nil
	}

	for i := range allDevices {
		if allDevices[i].IPAddress == ip {
			return &allDevices[i]
		}
	}

	return nil
}

func (s *Server) pushSpotifyTokenToDevice(deviceIP string, info spotify.ZeroConfInfo, username, accessToken string) error {
	host, port, err := net.SplitHostPort(deviceIP)
	if err != nil {
		// deviceIP has no port component — use the standard ZeroConf port.
		host = deviceIP
		port = "8200"
	}

	return spotify.PushSpotifyCredentialsWithInfo(host, port, info, username, accessToken)
}

// PrimeDeviceWithAmazon triggers an Amazon Music priming of the speaker if an Amazon account is linked.
func (s *Server) PrimeDeviceWithAmazon(deviceIP string) {
	s.mu.RLock()
	svc := s.amazonService
	s.mu.RUnlock()

	if svc == nil {
		return
	}

	accounts := svc.GetAccounts()
	if len(accounts) == 0 {
		return
	}

	accessToken, username, err := svc.GetFreshToken()
	if err != nil {
		log.Printf("[Amazon Watchdog] Failed to get fresh token for %s: %v", sanitizeLog(deviceIP), err)
		return
	}

	log.Printf("[Amazon Watchdog] Proactively priming %s with Amazon user %s", sanitizeLog(deviceIP), sanitizeLog(username))

	if err := s.pushAmazonTokenToDevice(deviceIP, username, accessToken); err != nil {
		if errors.Is(err, amazon.ErrAddUserNoOp) {
			log.Printf("[Amazon Watchdog] Successfully primed %s (ZeroConf addUser was an expected no-op)", sanitizeLog(deviceIP))
		} else {
			log.Printf("[Amazon Watchdog] Failed to prime %s: %v", sanitizeLog(deviceIP), err)
		}
	} else {
		log.Printf("[Amazon Watchdog] Successfully primed %s", sanitizeLog(deviceIP))
	}
}

func (s *Server) pushAmazonTokenToDevice(deviceIP, username, accessToken string) error {
	host, port, err := net.SplitHostPort(deviceIP)
	if err != nil {
		// deviceIP has no port component — use the standard ZeroConf port.
		host = deviceIP
		port = "8200"
	}

	return amazon.PushAmazonCredentials(host, port, username, accessToken)
}

func (s *Server) handleDiscoveredDevice(d models.DiscoveredDevice) {
	log.Printf("Discovered Bose device: %s at %s (Serial: %s)", sanitizeLog(d.Name), sanitizeLog(d.Host), sanitizeLog(d.SerialNo))

	// 1. Always fetch live device info from /info endpoint as the authoritative source
	liveInfo, err := s.sm.GetLiveDeviceInfo(d.Host)
	if err != nil {
		log.Printf("Failed to fetch live device info for %s at %s: %s", sanitizeLog(d.Name), sanitizeLog(d.Host), sanitizeErr(err))
		// Fallback to discovery info if /info is not available
		s.handleDiscoveredDeviceFallback(d)

		return
	}

	// 2. Use deviceID from /info as the canonical device identifier
	deviceID := liveInfo.DeviceID
	if deviceID == "" {
		log.Printf("No deviceID found in /info response for %s at %s, using fallback", sanitizeLog(d.Name), sanitizeLog(d.Host))
		s.handleDiscoveredDeviceFallback(d)

		return
	}

	log.Printf("Using deviceID '%s' from /info for device %s at %s", sanitizeLog(deviceID), sanitizeLog(d.Name), sanitizeLog(d.Host))

	// 3. Get account ID from live info or fallback to existing/default
	storedAccount := ""
	if existing := s.findExistingDeviceInfoByDeviceID(deviceID); existing != nil {
		storedAccount = existing.AccountID
	}

	accountID := liveInfo.MargeAccountUUID
	if accountID == "" {
		accountID = storedAccount
	}

	if accountID == "" {
		accountID = "default"
	}

	// If the speaker reports a paired account that differs from the stored
	// location, migrate the device directory so ListAllDevices doesn't return duplicates.
	if liveInfo.MargeAccountUUID != "" && storedAccount != "" && liveInfo.MargeAccountUUID != storedAccount {
		if err := s.ds.MoveDevice(storedAccount, accountID, deviceID); err != nil {
			log.Printf("Failed to migrate device %s from %s to %s: %v",
				sanitizeLog(deviceID), sanitizeLog(storedAccount), sanitizeLog(accountID), err)
		}
	}

	// 4. Get primary MAC address from networkInfo
	macAddress := liveInfo.GetPrimaryMacAddress()

	// 5. Build complete device info from live data
	info := &models.ServiceDeviceInfo{
		DeviceID:            deviceID, // Use deviceID from /info (MAC address)
		AccountID:           accountID,
		Name:                liveInfo.Name,                             // Use name from /info
		IPAddress:           d.Host,                                    // IP from discovery
		MacAddress:          macAddress,                                // MAC from /info networkInfo
		DeviceSerialNumber:  liveInfo.SerialNumber,                     // Serial from components
		ProductCode:         liveInfo.Type + " " + liveInfo.ModuleType, // Type + ModuleType
		FirmwareVersion:     liveInfo.SoftwareVer,
		ProductSerialNumber: "", // Will be populated from components if available
		DiscoveryMethod:     d.DiscoveryMethod,
	}

	// 6. Extract product serial number from PackagedProduct component
	for _, comp := range liveInfo.Components {
		if comp.Category == "PackagedProduct" && comp.SerialNumber != "" {
			info.ProductSerialNumber = comp.SerialNumber
			break
		}
	}

	// 7. Save the updated device info
	if err := s.ds.SaveDeviceInfo(accountID, deviceID, info); err != nil {
		log.Printf("Failed to save device info for %s: %s", sanitizeLog(deviceID), sanitizeErr(err))
		return
	}

	// If the device was (or needed to be) relocated to a different account, ensure the
	// stale source entry is gone. MoveDevice's rename is a no-op if the target already
	// existed (e.g. partial duplicate state), leaving the source dir behind; removing it
	// here is safe because SaveDeviceInfo above has already written fresh data to
	// accountID. RemoveDevice returns nil when the path does not exist, so this is also
	// a harmless no-op when MoveDevice already renamed the directory successfully.
	if storedAccount != "" && storedAccount != accountID {
		if err := s.ds.RemoveDevice(storedAccount, deviceID); err != nil {
			log.Printf("Failed to remove stale device entry for %s in %s: %v",
				sanitizeLog(deviceID), sanitizeLog(storedAccount), err)
		}
	}

	// 8. Create default Sources.xml only when no sources file exists yet
	if !s.ds.HasConfiguredSources(accountID, deviceID) {
		if sources, err := s.ds.GetConfiguredSources(accountID, deviceID); err == nil {
			log.Printf("Creating default Sources.xml for device %s", sanitizeLog(deviceID))

			if err := s.ds.SaveConfiguredSources(accountID, deviceID, sources); err != nil {
				log.Printf("Failed to save default sources for %s: %s", sanitizeLog(deviceID), sanitizeErr(err))
			}
		}
	}

	log.Printf("Successfully saved device %s (%s) with MAC-based deviceID: %s", sanitizeLog(info.Name), sanitizeLog(d.Host), sanitizeLog(deviceID))
}

// handleDiscoveredDeviceFallback handles device discovery when /info endpoint is not available
func (s *Server) handleDiscoveredDeviceFallback(d models.DiscoveredDevice) {
	log.Printf("Using fallback discovery method for device: %s at %s", sanitizeLog(d.Name), sanitizeLog(d.Host))

	// Use discovery data as-is with the old logic
	existingID := s.findExistingDeviceID(d)

	deviceID := d.SerialNo
	if deviceID == "" {
		deviceID = d.Host
	}

	accountID := "default"
	if existing := s.findExistingDeviceInfo(d); existing != nil {
		accountID = existing.AccountID
	}

	info := &models.ServiceDeviceInfo{
		DeviceID:           deviceID,
		AccountID:          accountID,
		Name:               d.Name,
		IPAddress:          d.Host,
		DeviceSerialNumber: d.SerialNo,
		ProductCode:        d.ModelID,
		FirmwareVersion:    "0.0.0", // Unknown from discovery
		DiscoveryMethod:    d.DiscoveryMethod,
	}

	// If we had an IP-based entry and now have a Serial, clean up the IP-based entry
	if d.SerialNo != "" && existingID != "" && existingID != d.SerialNo {
		log.Printf("Device %s previously known as %s, migrating to serial-based ID %s", sanitizeLog(d.Name), sanitizeLog(existingID), sanitizeLog(d.SerialNo))
		_ = s.ds.RemoveDevice(accountID, existingID)
	}

	if err := s.ds.SaveDeviceInfo(accountID, deviceID, info); err != nil {
		log.Printf("Failed to save device info for %s: %s", sanitizeLog(deviceID), sanitizeErr(err))
		return
	}

	// Create default Sources.xml only when no sources file exists yet
	if !s.ds.HasConfiguredSources(accountID, deviceID) {
		if sources, err := s.ds.GetConfiguredSources(accountID, deviceID); err == nil {
			log.Printf("Creating default Sources.xml for device %s (fallback)", sanitizeLog(deviceID))

			if err := s.ds.SaveConfiguredSources(accountID, deviceID, sources); err != nil {
				log.Printf("Failed to save default sources for %s: %s", sanitizeLog(deviceID), sanitizeErr(err))
			}
		}
	}

	log.Printf("Successfully saved device %s (%s) with fallback deviceID: %s", sanitizeLog(info.Name), sanitizeLog(d.Host), sanitizeLog(deviceID))
}

func (s *Server) mergeOverlappingDevices() {
	allDevices, err := s.ds.ListAllDevices()
	if err != nil {
		return
	}

	// Group devices by IP
	byIP := make(map[string][]models.ServiceDeviceInfo)

	for i := range allDevices {
		dev := allDevices[i]
		if dev.IPAddress != "" {
			byIP[dev.IPAddress] = append(byIP[dev.IPAddress], dev)
		}
	}

	for ip, devices := range byIP {
		if len(devices) <= 1 {
			continue
		}

		// We have multiple entries for the same IP.
		// Try to find one with a Serial Number to be the master.
		var master *models.ServiceDeviceInfo

		for i := range devices {
			if devices[i].DeviceSerialNumber != "" {
				master = &devices[i]
				break
			}
		}

		if master == nil {
			// Fallback: look for one with DeviceID that isn't the IP
			for i := range devices {
				if devices[i].DeviceID != "" && devices[i].DeviceID != devices[i].IPAddress {
					master = &devices[i]
					break
				}
			}
		}

		if master == nil {
			// None have serials, just keep the first one
			continue
		}

		masterID := master.DeviceID
		if masterID == "" {
			masterID = master.DeviceSerialNumber
		}

		for i := range devices {
			dev := devices[i]
			devID := dev.DeviceID

			if devID == "" {
				devID = dev.IPAddress
			}

			if devID != masterID && dev.IPAddress == ip {
				log.Printf("Merging overlapping device entry %s into %s (IP: %s)", sanitizeLog(devID), sanitizeLog(masterID), sanitizeLog(ip))
				_ = s.ds.RemoveDevice(dev.AccountID, devID)
			}
		}
	}
}

func (s *Server) findExistingDeviceID(d models.DiscoveredDevice) string {
	info := s.findExistingDeviceInfo(d)
	if info != nil {
		return info.DeviceID
	}

	return ""
}

func (s *Server) findExistingDeviceInfo(d models.DiscoveredDevice) *models.ServiceDeviceInfo {
	allDevices, _ := s.ds.ListAllDevices()
	for i := range allDevices {
		known := allDevices[i]
		// Match by Serial
		if d.SerialNo != "" && (known.DeviceID == d.SerialNo || known.DeviceSerialNumber == d.SerialNo) {
			return &known
		}
		// Match by IP
		if d.Host != "" && known.IPAddress == d.Host {
			return &known
		}
	}

	return nil
}

func (s *Server) resolveDeviceIDToIP(deviceID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 1. Try to find in Datastore
	devices, err := s.ds.ListAllDevices()
	if err == nil {
		for i := range devices {
			if devices[i].DeviceID == deviceID {
				return devices[i].IPAddress, nil
			}
		}
	}

	return "", fmt.Errorf("device not found: %s", deviceID)
}
