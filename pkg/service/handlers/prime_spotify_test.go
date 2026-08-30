package handlers

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
	"github.com/gesellix/bose-soundtouch/pkg/service/setup"
	"github.com/gesellix/bose-soundtouch/pkg/service/spotify"
	"github.com/go-chi/chi/v5"
)

// TestPrimeDeviceWithSpotify_RegistersMargeSource is a regression test for the
// "AddPreset - failed due to invalid SourceID" failure observed when storing a
// Spotify preset on a primed device. The watchdog priming path used to push
// ZeroConf credentials without writing a SPOTIFY ConfiguredSource into the
// marge datastore — so marge.UpdatePreset later had nothing to match
// SourceID="SPOTIFY" against and rejected the storePreset request.
//
// This test verifies that PrimeDeviceWithSpotify now also calls marge.AddSource
// for the device's account, producing a ConfiguredSource with
// SourceProviderID="15" (constants.SpotifyProviderID).
func TestPrimeDeviceWithSpotify_RegistersMargeSource(t *testing.T) {
	tmpDir := t.TempDir()
	ds := datastore.NewDataStore(tmpDir)
	server := NewServer(ds, nil, "http://localhost", false, false, false)

	// Fake speaker that accepts the ZeroConf push and records whether
	// /notification (sourcesUpdated) was hit.
	var notified atomic.Bool
	var getInfoCalls atomic.Int32
	var addUserCalls atomic.Int32

	speakerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notification" {
			notified.Store(true)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" ?><status>/notification</status>`)

			return
		}
		if r.URL.Path == "/sources" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" ?><sources><sourceItem source="SPOTIFY" sourceAccount="spotify-user" status="READY">Spotify User</sourceItem></sources>`)
			return
		}

		switch r.URL.Query().Get("action") {
		case "getInfo":
			getInfoCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"publicKey":  base64.StdEncoding.EncodeToString(make([]byte, 96)),
				"activeUser": "spotify-user",
			})
		case "addUser":
			addUserCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer speakerTS.Close()

	speakerHostPort := strings.TrimPrefix(speakerTS.URL, "http://")

	speakerHost, _, err := net.SplitHostPort(speakerHostPort)
	if err != nil {
		t.Fatalf("split speaker URL: %v", err)
	}

	// Register the device under a real account so the IP→account lookup succeeds.
	const accountID = "acc-prime"
	const deviceID = "DEVPRIME"
	devInfo := &models.ServiceDeviceInfo{
		DeviceID:  deviceID,
		AccountID: accountID,
		Name:      "Test Speaker",
		IPAddress: speakerHost,
	}

	if err := ds.SaveDeviceInfo(accountID, deviceID, devInfo); err != nil {
		t.Fatalf("SaveDeviceInfo: %v", err)
	}
	if err := ds.SaveConfiguredSources(accountID, deviceID, []models.ConfiguredSource{
		spotifyConfiguredSourceForTest("spotify-user", "bs-deadbeefdeadbeefdeadbeefdeadbeef"),
	}); err != nil {
		t.Fatalf("SaveConfiguredSources: %v", err)
	}

	// marge.AddSource walks the account/devices dir — make sure the per-device
	// subdir exists so the source actually gets persisted.
	if err := os.MkdirAll(filepath.Join(ds.AccountDevicesDir(accountID), deviceID), 0o755); err != nil {
		t.Fatalf("MkdirAll device dir: %v", err)
	}

	// Pre-seed a linked Spotify account so PrimeDeviceWithSpotify has something
	// to push. The token is valid for an hour so GetFreshToken won't try to
	// refresh against a live endpoint. We point the token endpoint at a noop
	// URL just in case, so a stray refresh would fail loudly rather than fan
	// out to the internet.
	spotifyDir := filepath.Join(tmpDir, "spotify")
	if err := os.MkdirAll(spotifyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll spotify dir: %v", err)
	}

	accountsPayload := map[string]map[string]any{
		"spotify-user": {
			"user_id":       "spotify-user",
			"display_name":  "Spotify User",
			"email":         "user@example.com",
			"access_token":  "fresh-access-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(time.Hour).Unix(),
			"bose_secret":   "bs-deadbeefdeadbeefdeadbeefdeadbeef",
		},
	}
	accountsJSON, err := json.Marshal(accountsPayload)
	if err != nil {
		t.Fatalf("marshal accounts: %v", err)
	}

	if err := os.WriteFile(filepath.Join(spotifyDir, "accounts.json"), accountsJSON, 0o600); err != nil {
		t.Fatalf("write accounts.json: %v", err)
	}

	ss := spotify.NewSpotifyService("client-id", "client-secret", "http://localhost/callback", tmpDir)
	// Unused fallback token endpoint — defensive in case the test ever drifts
	// to an expired token.
	ss.SetEndpoints("http://127.0.0.1:1/token", "http://127.0.0.1:1")

	if err := ss.Load(); err != nil {
		t.Fatalf("Load spotify accounts: %v", err)
	}

	if len(ss.GetAccounts()) != 1 {
		t.Fatalf("expected 1 spotify account after Load, got %d", len(ss.GetAccounts()))
	}

	server.SetSpotifyService(ss)
	server.spotifyPrimeReadbackDelays = []time.Duration{0}

	// Priming is fail-closed and therefore requires an exact pre-existing source binding.
	sources, _ := ds.GetConfiguredSources(accountID, deviceID)
	if !hasSpotifySource(sources) {
		t.Fatalf("precondition failed: exact SPOTIFY source is missing before priming")
	}

	// Pass host:port so the ZeroConf push hits our test server instead of the
	// hard-coded :8200 fallback. The IP→account lookup strips the port before
	// matching against devInfo.IPAddress.
	result := server.PrimeDeviceWithSpotify(speakerHostPort)
	if result.Outcome != "confirmed" || result.UserID != "spotify-user" || !result.WriteAttempted {
		t.Fatalf("prime result = %+v", result)
	}
	if got := getInfoCalls.Load(); got != 2 {
		t.Fatalf("getInfo calls = %d, want 2 (initial plus readback)", got)
	}
	if got := addUserCalls.Load(); got != 1 {
		t.Fatalf("addUser calls = %d, want 1", got)
	}

	sources, err = ds.GetConfiguredSources(accountID, deviceID)
	if err != nil {
		t.Fatalf("GetConfiguredSources after priming: %v", err)
	}

	if !hasSpotifySource(sources) {
		for _, src := range sources {
			t.Logf("source after priming: ID=%s providerID=%s keyType=%s account=%s", src.ID, src.SourceProviderID, src.SourceKey.Type, src.SourceKey.Account)
		}

		t.Fatalf("expected a SPOTIFY ConfiguredSource (providerID=%d) after priming", constants.SpotifyProviderID)
	}

	// The speaker's on-device Sources.xml only refreshes when we tell it to —
	// without this notification storePreset keeps failing even though marge
	// already has the SPOTIFY source.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && !notified.Load() {
		time.Sleep(20 * time.Millisecond)
	}

	if !notified.Load() {
		t.Errorf("speaker did not receive a sourcesUpdated /notification after priming")
	}
}

func TestRefreshSpotifySourceAfterPrimeCannotReplaceNewerAccountBinding(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	const (
		accountID = "marge-a"
		deviceID  = "device-a"
	)
	if err := ds.SaveDeviceInfo(accountID, deviceID, &models.ServiceDeviceInfo{
		DeviceID:  deviceID,
		AccountID: accountID,
		IPAddress: "127.0.0.1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ds.SaveConfiguredSources(accountID, deviceID, []models.ConfiguredSource{
		spotifyConfiguredSourceForTest("user-b", spotifyUserBSecret),
	}); err != nil {
		t.Fatal(err)
	}

	s := NewServer(ds, nil, "http://localhost", false, false, false)
	svc := spotifyServiceForHandlerTest(t, dir)
	s.SetSpotifyService(svc)
	oldAccount, ok := svc.GetLinkedAccount("user-a")
	if !ok {
		t.Fatal("old linked Spotify identity is missing")
	}

	err := s.refreshSpotifySourceAfterPrime("127.0.0.1:1", accountID, deviceID, oldAccount)
	if err == nil || !strings.Contains(err.Error(), "ownership changed") {
		t.Fatalf("refresh error = %v, want ownership change", err)
	}
	sources, err := ds.GetConfiguredSources(accountID, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindingFromSources(accountID, deviceID, sources)
	if err != nil {
		t.Fatal(err)
	}
	if binding.UserID != "user-b" || binding.Secret != spotifyUserBSecret {
		t.Fatalf("newer binding was replaced: %+v", binding)
	}
}

// TestPrimeDeviceWithSpotify_SkipsWhenDeviceUnmapped ensures that priming a
// device whose IP is not associated with any account does NOT fabricate a
// source under the "default" account — the previous behavior would silently
// pollute marge with sources for devices that never asked.
func TestPrimeDeviceWithSpotify_SkipsWhenDeviceUnmapped(t *testing.T) {
	tmpDir := t.TempDir()
	ds := datastore.NewDataStore(tmpDir)
	server := NewServer(ds, nil, "http://localhost", false, false, false)

	speakerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "getInfo":
			http.Error(w, "not supported", http.StatusNotFound)
		case "addUser":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer speakerTS.Close()

	speakerURL, _ := url.Parse(speakerTS.URL)
	speakerHostPort := speakerURL.Host

	// Pre-seed a Spotify account but do NOT register any device.
	spotifyDir := filepath.Join(tmpDir, "spotify")
	_ = os.MkdirAll(spotifyDir, 0o755)
	accountsPayload := map[string]map[string]any{
		"spotify-user": {
			"user_id":      "spotify-user",
			"display_name": "Spotify User",
			"access_token": "fresh-access-token",
			"expires_at":   time.Now().Add(time.Hour).Unix(),
			"bose_secret":  "bs-deadbeef",
		},
	}

	accountsJSON, err := json.Marshal(accountsPayload)
	if err != nil {
		t.Fatalf("marshal accounts: %v", err)
	}

	_ = os.WriteFile(filepath.Join(spotifyDir, "accounts.json"), accountsJSON, 0o600)

	ss := spotify.NewSpotifyService("client-id", "client-secret", "http://localhost/callback", tmpDir)
	ss.SetEndpoints("http://127.0.0.1:1/token", "http://127.0.0.1:1")

	if err := ss.Load(); err != nil {
		t.Fatalf("Load spotify accounts: %v", err)
	}

	server.SetSpotifyService(ss)

	server.PrimeDeviceWithSpotify(speakerHostPort)

	// "default" account should have no SPOTIFY source added by us.
	sources, _ := ds.GetConfiguredSources("default", "")
	if hasSpotifySource(sources) {
		t.Errorf("priming an unmapped device wrote a SPOTIFY source under 'default' — should have been skipped")
	}
}

// TestPrimeDeviceWithSpotify_LiveMargeAccountUUIDWins covers the production
// scenario the previous test didn't catch: a device whose datastore
// ServiceDeviceInfo.AccountID is "default" (or stale) but whose live
// :8090/info reports a real paired margeAccountUUID. The SPOTIFY source must
// land under the paired account — that's the account marge.UpdatePreset
// receives storePreset under, so writing anywhere else means the preset still
// fails with "AddPreset - failed due to invalid SourceID".
//
// Mirrors setup.populateDeviceInfo's resolution order (datastore ← live /info)
// rather than guessing.
func TestPrimeDeviceWithSpotify_LiveMargeAccountUUIDWins(t *testing.T) {
	tmpDir := t.TempDir()
	ds := datastore.NewDataStore(tmpDir)
	server := NewServer(ds, nil, "http://localhost", false, false, false)

	const (
		datastoreAccount = "default" // stale / fallback
		pairedAccount    = "1111111" // live margeAccountUUID from /info
		deviceID         = "DEVPAIR"
	)

	// Fake speaker that serves both /info and the ZeroConf /zc.
	var speakerHost string
	var getInfoCalls atomic.Int32
	var addUserCalls atomic.Int32

	speakerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/info"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" ?>`+
				`<info deviceID="`+deviceID+`">`+
				`<name>Paired Speaker</name><type>SoundTouch 20</type>`+
				`<margeAccountUUID>`+pairedAccount+`</margeAccountUUID>`+
				`</info>`)
		case r.URL.Path == "/notification":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" ?><status>/notification</status>`)
		case r.URL.Path == "/sources":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" ?><sources><sourceItem source="SPOTIFY" sourceAccount="spotify-user" status="READY">Spotify User</sourceItem></sources>`)
		default:
			switch r.URL.Query().Get("action") {
			case "getInfo":
				getInfoCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"publicKey":  base64.StdEncoding.EncodeToString(make([]byte, 96)),
					"activeUser": "spotify-user",
				})
			case "addUser":
				addUserCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			default:
				http.NotFound(w, r)
			}
		}
	}))
	defer speakerTS.Close()

	speakerHostPort := strings.TrimPrefix(speakerTS.URL, "http://")
	speakerHost, _, _ = net.SplitHostPort(speakerHostPort)

	// Register the device under the STALE account so the datastore lookup
	// would yield the wrong answer if used in isolation.
	devInfo := &models.ServiceDeviceInfo{
		DeviceID:  deviceID,
		AccountID: datastoreAccount,
		Name:      "Paired Speaker",
		IPAddress: speakerHost,
	}
	if err := ds.SaveDeviceInfo(datastoreAccount, deviceID, devInfo); err != nil {
		t.Fatalf("SaveDeviceInfo: %v", err)
	}

	// And make sure the paired account's device dir exists so
	// marge.AddSource can persist the source (it walks accounts/devices/...).
	if err := os.MkdirAll(filepath.Join(ds.AccountDevicesDir(pairedAccount), deviceID), 0o755); err != nil {
		t.Fatalf("MkdirAll paired dir: %v", err)
	}
	if err := ds.SaveConfiguredSources(pairedAccount, deviceID, []models.ConfiguredSource{
		spotifyConfiguredSourceForTest("spotify-user", "bs-deadbeefdeadbeefdeadbeefdeadbeef"),
	}); err != nil {
		t.Fatalf("SaveConfiguredSources paired binding: %v", err)
	}

	// Pre-seed a Spotify account so priming has something to push.
	spotifyDir := filepath.Join(tmpDir, "spotify")
	_ = os.MkdirAll(spotifyDir, 0o755)
	accountsPayload := map[string]map[string]any{
		"spotify-user": {
			"user_id":       "spotify-user",
			"display_name":  "Spotify User",
			"access_token":  "fresh-access-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(time.Hour).Unix(),
			"bose_secret":   "bs-deadbeefdeadbeefdeadbeefdeadbeef",
		},
	}

	accountsJSON, err := json.Marshal(accountsPayload)
	if err != nil {
		t.Fatalf("marshal accounts: %v", err)
	}

	_ = os.WriteFile(filepath.Join(spotifyDir, "accounts.json"), accountsJSON, 0o600)

	ss := spotify.NewSpotifyService("client-id", "client-secret", "http://localhost/callback", tmpDir)
	ss.SetEndpoints("http://127.0.0.1:1/token", "http://127.0.0.1:1")

	if err := ss.Load(); err != nil {
		t.Fatalf("Load spotify accounts: %v", err)
	}

	server.SetSpotifyService(ss)
	server.spotifyPrimeReadbackDelays = []time.Duration{0}

	// Wire a real setup.Manager so resolvePairedAccount reaches /info.
	// HTTPGet uses the default net/http client, which hits the httptest
	// server directly via deviceIP=host:port.
	server.sm = setup.NewManager("http://localhost", ds, nil)

	result := server.PrimeDeviceWithSpotify(speakerHostPort)
	if result.Outcome != "confirmed" || result.UserID != "spotify-user" || !result.WriteAttempted {
		t.Fatalf("prime result = %+v", result)
	}
	if got := getInfoCalls.Load(); got != 2 {
		t.Fatalf("getInfo calls = %d, want 2 (initial plus readback)", got)
	}
	if got := addUserCalls.Load(); got != 1 {
		t.Fatalf("addUser calls = %d, want 1", got)
	}

	// SPOTIFY source must be under the PAIRED account, not the datastore one.
	pairedSources, err := ds.GetConfiguredSources(pairedAccount, deviceID)
	if err != nil {
		t.Fatalf("GetConfiguredSources(paired): %v", err)
	}

	if !hasSpotifySource(pairedSources) {
		t.Errorf("expected SPOTIFY source under paired account %s, got %d sources", pairedAccount, len(pairedSources))
	}

	// And it must NOT have been written under the stale datastore account.
	staleSources, _ := ds.GetConfiguredSources(datastoreAccount, deviceID)
	if hasSpotifySource(staleSources) {
		t.Errorf("SPOTIFY source unexpectedly written under stale datastore account %s — should follow live margeAccountUUID", datastoreAccount)
	}
}

type spotifyPrimeScript struct {
	activeUser     func(getInfoCall int) string
	addUserStatus  int
	getInfoEntered chan<- struct{}
	releaseGetInfo <-chan struct{}
	addUserEntered chan<- struct{}
	releaseAddUser <-chan struct{}
	sourcesEntered chan<- struct{}
	releaseSources <-chan struct{}
}

type spotifyPrimeHarness struct {
	server       *Server
	service      *spotify.Service
	device       string
	getInfoCalls atomic.Int32
	addUserCalls atomic.Int32
	actionsMu    sync.Mutex
	actions      []string
}

func newSpotifyPrimeHarness(t *testing.T, delays []time.Duration, script spotifyPrimeScript) *spotifyPrimeHarness {
	t.Helper()

	const (
		accountID = "prime-account"
		deviceID  = "PRIMEDEVICE"
		userID    = "spotify-user"
		secret    = "bs-deadbeefdeadbeefdeadbeefdeadbeef"
	)

	h := &spotifyPrimeHarness{}
	var getInfoEnteredOnce sync.Once
	var addUserEnteredOnce sync.Once
	var sourcesEnteredOnce sync.Once
	speakerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notification":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" ?><status>/notification</status>`)
			return
		case "/sources":
			pause := false
			sourcesEnteredOnce.Do(func() {
				pause = true
				if script.sourcesEntered != nil {
					close(script.sourcesEntered)
				}
			})
			if pause && script.releaseSources != nil {
				<-script.releaseSources
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" ?><sources><sourceItem source="SPOTIFY" sourceAccount="spotify-user" status="READY">Spotify User</sourceItem></sources>`)
			return
		}

		switch r.URL.Query().Get("action") {
		case "getInfo":
			call := int(h.getInfoCalls.Add(1))
			h.recordAction("getInfo")
			if call == 1 && script.getInfoEntered != nil {
				getInfoEnteredOnce.Do(func() { close(script.getInfoEntered) })
			}
			if call == 1 && script.releaseGetInfo != nil {
				<-script.releaseGetInfo
			}
			activeUser := ""
			if script.activeUser != nil {
				activeUser = script.activeUser(call)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"publicKey":  base64.StdEncoding.EncodeToString(make([]byte, 96)),
				"activeUser": activeUser,
			})
		case "addUser":
			h.addUserCalls.Add(1)
			h.recordAction("addUser")
			if script.addUserEntered != nil {
				addUserEnteredOnce.Do(func() { close(script.addUserEntered) })
			}
			if script.releaseAddUser != nil {
				<-script.releaseAddUser
			}
			status := script.addUserStatus
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(speakerTS.Close)

	h.device = strings.TrimPrefix(speakerTS.URL, "http://")
	host, _, err := net.SplitHostPort(h.device)
	if err != nil {
		t.Fatalf("split speaker URL: %v", err)
	}

	tmpDir := t.TempDir()
	ds := datastore.NewDataStore(tmpDir)
	if err := ds.SaveDeviceInfo(accountID, deviceID, &models.ServiceDeviceInfo{
		DeviceID:  deviceID,
		AccountID: accountID,
		Name:      "Prime Speaker",
		IPAddress: host,
	}); err != nil {
		t.Fatalf("SaveDeviceInfo: %v", err)
	}
	if err := ds.SaveConfiguredSources(accountID, deviceID, []models.ConfiguredSource{
		spotifyConfiguredSourceForTest(userID, secret),
	}); err != nil {
		t.Fatalf("SaveConfiguredSources: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(ds.AccountDevicesDir(accountID), deviceID), 0o755); err != nil {
		t.Fatalf("MkdirAll device dir: %v", err)
	}

	spotifyDir := filepath.Join(tmpDir, "spotify")
	if err := os.MkdirAll(spotifyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll spotify dir: %v", err)
	}
	accountsJSON, err := json.Marshal(map[string]map[string]any{
		userID: {
			"user_id":       userID,
			"display_name":  "Spotify User",
			"access_token":  "fresh-access-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(time.Hour).Unix(),
			"bose_secret":   secret,
		},
	})
	if err != nil {
		t.Fatalf("marshal accounts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(spotifyDir, "accounts.json"), accountsJSON, 0o600); err != nil {
		t.Fatalf("write accounts.json: %v", err)
	}

	ss := spotify.NewSpotifyService("client-id", "client-secret", "http://localhost/callback", tmpDir)
	ss.SetEndpoints("http://127.0.0.1:1/token", "http://127.0.0.1:1")
	if err := ss.Load(); err != nil {
		t.Fatalf("Load spotify accounts: %v", err)
	}

	h.server = NewServer(ds, nil, "http://localhost", false, false, false)
	h.server.SetSpotifyService(ss)
	h.service = ss
	h.server.spotifyPrimeReadbackDelays = append([]time.Duration(nil), delays...)
	return h
}

func (h *spotifyPrimeHarness) recordAction(action string) {
	h.actionsMu.Lock()
	defer h.actionsMu.Unlock()
	h.actions = append(h.actions, action)
}

func (h *spotifyPrimeHarness) actionSnapshot() []string {
	h.actionsMu.Lock()
	defer h.actionsMu.Unlock()
	return append([]string(nil), h.actions...)
}

func TestPrimeDeviceWithSpotify_OneWriteThenDelayedConfirmation(t *testing.T) {
	h := newSpotifyPrimeHarness(t, []time.Duration{0, 5 * time.Millisecond}, spotifyPrimeScript{
		activeUser: func(call int) string {
			if call < 3 {
				return ""
			}
			return "spotify-user"
		},
	})

	result := h.server.PrimeDeviceWithSpotify(h.device)
	if result.Outcome != "confirmed" || !result.WriteAttempted {
		t.Fatalf("prime result = %+v, want confirmed write", result)
	}
	if got := h.getInfoCalls.Load(); got != 3 {
		t.Fatalf("getInfo calls = %d, want 3 (initial plus two bounded readbacks)", got)
	}
	if got := h.addUserCalls.Load(); got != 1 {
		t.Fatalf("addUser calls = %d, want 1", got)
	}
	if got := h.actionSnapshot(); len(got) < 2 || got[0] != "getInfo" || got[1] != "addUser" {
		t.Fatalf("initial actions = %v, want [getInfo addUser]", got)
	}
}

func TestPrimeDeviceWithSpotify_ForeignActiveUserFailsWithoutWrite(t *testing.T) {
	h := newSpotifyPrimeHarness(t, []time.Duration{0}, spotifyPrimeScript{
		activeUser: func(int) string { return "foreign-user" },
	})

	result := h.server.PrimeDeviceWithSpotify(h.device)
	if result.Outcome != "failed" || result.WriteAttempted {
		t.Fatalf("prime result = %+v, want failure without write", result)
	}
	if got := h.getInfoCalls.Load(); got != 1 {
		t.Fatalf("getInfo calls = %d, want 1", got)
	}
	if got := h.addUserCalls.Load(); got != 0 {
		t.Fatalf("addUser calls = %d, want 0", got)
	}
}

func TestPrimeDeviceWithSpotify_RevalidatesOwnershipBeforeCredentialWrite(t *testing.T) {
	getInfoEntered := make(chan struct{})
	releaseGetInfo := make(chan struct{})
	h := newSpotifyPrimeHarness(t, []time.Duration{0}, spotifyPrimeScript{
		activeUser:     func(int) string { return "" },
		getInfoEntered: getInfoEntered,
		releaseGetInfo: releaseGetInfo,
	})

	resultCh := make(chan SpotifyPrimeResult, 1)
	go func() { resultCh <- h.server.PrimeDeviceWithSpotify(h.device) }()
	select {
	case <-getInfoEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for priming getInfo pause")
	}

	h.server.spotifySourceMu.Lock()
	err := h.server.ds.SaveConfiguredSources("prime-account", "PRIMEDEVICE", []models.ConfiguredSource{
		spotifyConfiguredSourceForTest("newer-user", spotifyUserBSecret),
	})
	h.server.spotifySourceMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	close(releaseGetInfo)

	select {
	case result := <-resultCh:
		if result.Outcome != "failed" || result.WriteAttempted || !strings.Contains(result.Detail, "ownership changed") {
			t.Fatalf("prime result = %+v, want stale ownership failure before write", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale prime result")
	}
	if got := h.addUserCalls.Load(); got != 0 {
		t.Fatalf("stale prime wrote obsolete credentials %d time(s)", got)
	}
}

func TestConfiguredSourceHandlerCannotChangeOwnershipDuringCredentialWrite(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       func(string) string
		body       string
		wantStatus int
	}{
		{
			name:       "account POST",
			method:     http.MethodPost,
			path:       func(string) string { return "/streaming/account/prime-account/source" },
			body:       `<source><username>newer-user</username><sourceproviderid>15</sourceproviderid><credential type="token_version_3">` + spotifyUserBSecret + `</credential><sourcename>Newer User</sourcename></source>`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "account DELETE",
			method:     http.MethodDelete,
			path:       func(sourceID string) string { return "/streaming/account/prime-account/source/" + sourceID },
			wantStatus: http.StatusOK,
		},
		{
			name:       "device DELETE",
			method:     http.MethodDelete,
			path:       func(sourceID string) string { return "/setup/sources/prime-account/PRIMEDEVICE/" + sourceID },
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addUserEntered := make(chan struct{})
			releaseAddUser := make(chan struct{})
			h := newSpotifyPrimeHarness(t, []time.Duration{0}, spotifyPrimeScript{
				activeUser:     func(int) string { return "" },
				addUserEntered: addUserEntered,
				releaseAddUser: releaseAddUser,
			})
			lockEvents := observeSpotifySourceLocks(h.server)

			sources, err := h.server.ds.GetConfiguredSources("prime-account", "PRIMEDEVICE")
			if err != nil || len(sources) == 0 || sources[0].ID == "" {
				t.Fatalf("configured-source precondition = %+v, %v", sources, err)
			}
			requestPath := tc.path(sources[0].ID)

			primeDone := make(chan SpotifyPrimeResult, 1)
			go func() { primeDone <- h.server.PrimeDeviceWithSpotify(h.device) }()
			select {
			case <-addUserEntered:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for credential write pause")
			}
			awaitSpotifySourceLockEvent(t, lockEvents, 1, false)
			awaitSpotifySourceLockEvent(t, lockEvents, 1, true)

			router := chi.NewRouter()
			router.Post("/streaming/account/{account}/source", h.server.HandleMargeAddSource)
			router.Delete("/streaming/account/{account}/source/{sourceID}", h.server.HandleMargeDeleteSource)
			router.Delete("/setup/sources/{account}/{device}/{sourceID}", h.server.HandleDeleteSource)
			req := httptest.NewRequest(tc.method, requestPath, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			handlerDone := make(chan struct{})
			go func() {
				router.ServeHTTP(rec, req)
				close(handlerDone)
			}()
			awaitSpotifySourceLockEvent(t, lockEvents, 2, false)
			if h.server.spotifySourceMu.Mutex.TryLock() {
				h.server.spotifySourceMu.Mutex.Unlock()
				t.Fatalf("configured-source %s did not contend on spotifySourceMu", tc.method)
			}
			sources, err = h.server.ds.GetConfiguredSources("prime-account", "PRIMEDEVICE")
			if err != nil {
				t.Fatal(err)
			}
			binding, err := bindingFromSources("prime-account", "PRIMEDEVICE", sources)
			if err != nil || binding.UserID != "spotify-user" {
				t.Fatalf("source ownership changed before the credential write completed: %+v, %v", binding, err)
			}

			close(releaseAddUser)
			awaitSpotifySourceLockEvent(t, lockEvents, 2, true)
			select {
			case <-handlerDone:
			case <-time.After(time.Second):
				t.Fatalf("timed out waiting for configured-source %s", tc.method)
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("configured-source %s status = %d: %s", tc.method, rec.Code, rec.Body.String())
			}
			select {
			case <-primeDone:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for priming result")
			}
			if got := h.addUserCalls.Load(); got != 1 {
				t.Fatalf("credential writes = %d, want 1", got)
			}
		})
	}
}

func prepareSameIdentitySpotifyReauthorization(t *testing.T, h *spotifyPrimeHarness) (spotifyOAuthTransaction, func()) {
	t.Helper()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = io.WriteString(w, `{"access_token":"reauthorized-access","refresh_token":"refresh-token","expires_in":3600}`)
		case "/me":
			_, _ = io.WriteString(w, `{"id":"spotify-user","display_name":"Reauthorized User"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	h.service.SetEndpoints(api.URL+"/token", api.URL)
	state, _, err := h.server.newSpotifyOAuthTransaction("prime-account")
	if err != nil {
		api.Close()
		t.Fatal(err)
	}
	h.server.spotifyOAuthMu.Lock()
	transaction := h.server.spotifyOAuthTransactions[state]
	h.server.spotifyOAuthMu.Unlock()

	return transaction, api.Close
}

func TestPrimeDeviceWithSpotify_SameIdentityGenerationChangesBeforeCredentialWrite(t *testing.T) {
	getInfoEntered := make(chan struct{})
	releaseGetInfo := make(chan struct{})
	h := newSpotifyPrimeHarness(t, []time.Duration{0}, spotifyPrimeScript{
		activeUser:     func(int) string { return "" },
		getInfoEntered: getInfoEntered,
		releaseGetInfo: releaseGetInfo,
	})
	transaction, closeAPI := prepareSameIdentitySpotifyReauthorization(t, h)
	defer closeAPI()

	resultCh := make(chan SpotifyPrimeResult, 1)
	go func() { resultCh <- h.server.PrimeDeviceWithSpotify(h.device) }()
	select {
	case <-getInfoEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial getInfo pause")
	}
	if _, _, err := h.server.exchangeAndPublishSpotifyAuthorization(h.service, transaction, "reauthorize"); err != nil {
		t.Fatal(err)
	}
	close(releaseGetInfo)

	select {
	case result := <-resultCh:
		if result.Outcome != "failed" || result.WriteAttempted || !strings.Contains(result.Detail, "ownership changed") {
			t.Fatalf("prime result = %+v, want stale-generation failure before write", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for priming result")
	}
	if got := h.addUserCalls.Load(); got != 0 {
		t.Fatalf("stale generation wrote credentials %d time(s)", got)
	}
}

func TestPrimeDeviceWithSpotify_SameIdentityGenerationChangesDuringSourceReadback(t *testing.T) {
	sourcesEntered := make(chan struct{})
	releaseSources := make(chan struct{})
	h := newSpotifyPrimeHarness(t, []time.Duration{0}, spotifyPrimeScript{
		activeUser: func(call int) string {
			if call == 1 {
				return ""
			}
			return "spotify-user"
		},
		sourcesEntered: sourcesEntered,
		releaseSources: releaseSources,
	})
	transaction, closeAPI := prepareSameIdentitySpotifyReauthorization(t, h)
	defer closeAPI()

	resultCh := make(chan SpotifyPrimeResult, 1)
	go func() { resultCh <- h.server.PrimeDeviceWithSpotify(h.device) }()
	select {
	case <-sourcesEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for final source readback pause")
	}
	if _, _, err := h.server.exchangeAndPublishSpotifyAuthorization(h.service, transaction, "reauthorize"); err != nil {
		t.Fatal(err)
	}
	close(releaseSources)

	select {
	case result := <-resultCh:
		if result.Outcome != "unverified" || !result.WriteAttempted || result.Outcome == "confirmed" {
			t.Fatalf("prime result = %+v, want unverified after readback generation change", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for priming result")
	}
}

func TestPrimeDeviceWithSpotify_AddUserNoOpRemainsUnverified(t *testing.T) {
	h := newSpotifyPrimeHarness(t, []time.Duration{0}, spotifyPrimeScript{
		activeUser: func(call int) string {
			if call == 1 {
				return ""
			}
			return "spotify-user"
		},
		addUserStatus: http.StatusNotFound,
	})

	result := h.server.PrimeDeviceWithSpotify(h.device)
	if result.Outcome != "unverified" || !result.WriteAttempted {
		t.Fatalf("prime result = %+v, want unverified attempted write", result)
	}
	if got := h.getInfoCalls.Load(); got != 2 {
		t.Fatalf("getInfo calls = %d, want initial plus target-user readback", got)
	}
	if got := h.addUserCalls.Load(); got != 1 {
		t.Fatalf("addUser calls = %d, want 1", got)
	}
}

func TestPrimeDeviceWithSpotify_NoMatchingReadbackIsUnverified(t *testing.T) {
	h := newSpotifyPrimeHarness(t, []time.Duration{0, 2 * time.Millisecond}, spotifyPrimeScript{
		activeUser: func(int) string { return "" },
	})

	result := h.server.PrimeDeviceWithSpotify(h.device)
	if result.Outcome != "unverified" || !result.WriteAttempted {
		t.Fatalf("prime result = %+v, want unverified attempted write", result)
	}
	if got := h.getInfoCalls.Load(); got != 3 {
		t.Fatalf("getInfo calls = %d, want initial plus two bounded readbacks", got)
	}
	if got := h.addUserCalls.Load(); got != 1 {
		t.Fatalf("addUser calls = %d, want 1", got)
	}
}

func TestPrimeDeviceWithSpotify_ConcurrentRequestsCoalesce(t *testing.T) {
	addUserEntered := make(chan struct{})
	releaseAddUser := make(chan struct{})
	h := newSpotifyPrimeHarness(t, []time.Duration{0}, spotifyPrimeScript{
		activeUser: func(call int) string {
			if call == 1 {
				return ""
			}
			return "spotify-user"
		},
		addUserEntered: addUserEntered,
		releaseAddUser: releaseAddUser,
	})

	const requests = 8
	start := make(chan struct{})
	ready := make(chan struct{}, requests)
	results := make(chan SpotifyPrimeResult, requests)
	for range requests {
		go func() {
			ready <- struct{}{}
			<-start
			results <- h.server.PrimeDeviceWithSpotify(h.device)
		}()
	}
	for range requests {
		<-ready
	}
	close(start)
	select {
	case <-addUserEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for addUser")
	}
	time.Sleep(20 * time.Millisecond)
	close(releaseAddUser)

	for range requests {
		select {
		case result := <-results:
			if result.Outcome != "confirmed" || !result.WriteAttempted {
				t.Errorf("prime result = %+v, want confirmed shared write", result)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for coalesced prime result")
		}
	}
	if got := h.addUserCalls.Load(); got != 1 {
		t.Fatalf("addUser calls = %d, want 1", got)
	}
	if got := h.getInfoCalls.Load(); got != 2 {
		t.Fatalf("getInfo calls = %d, want one initial read and one readback", got)
	}
}

func hasSpotifySource(sources []models.ConfiguredSource) bool {
	for _, src := range sources {
		if src.SourceProviderID == "15" || src.SourceKey.Type == constants.ProviderSpotify {
			return true
		}
	}

	return false
}
