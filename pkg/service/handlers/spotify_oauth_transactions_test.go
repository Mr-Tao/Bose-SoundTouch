package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
	"github.com/gesellix/bose-soundtouch/pkg/service/spotify"
)

type failingSpotifyOAuthRandom struct{}

func (failingSpotifyOAuthRandom) Read([]byte) (int, error) {
	return 0, errors.New("random source failed")
}

type spotifySourceLockEvent struct {
	call     uint64
	acquired bool
}

func observeSpotifySourceLocks(s *Server) <-chan spotifySourceLockEvent {
	events := make(chan spotifySourceLockEvent, 16)
	s.spotifySourceMu.observe = func(call uint64, acquired bool) {
		events <- spotifySourceLockEvent{call: call, acquired: acquired}
	}

	return events
}

func awaitSpotifySourceLockEvent(t *testing.T, events <-chan spotifySourceLockEvent, call uint64, acquired bool) {
	t.Helper()
	select {
	case event := <-events:
		if event != (spotifySourceLockEvent{call: call, acquired: acquired}) {
			t.Fatalf("Spotify source lock event = %+v, want call=%d acquired=%v", event, call, acquired)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Spotify source lock call=%d acquired=%v", call, acquired)
	}
}

func newSpotifyOAuthTestServer(t *testing.T, redirectURI string) *Server {
	t.Helper()

	ds := datastore.NewDataStore(t.TempDir())
	if err := ds.SaveDeviceInfo("marge-a", "device-a", &models.ServiceDeviceInfo{DeviceID: "device-a", AccountID: "marge-a"}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(ds, nil, "http://127.0.0.1", false, false, false)
	s.SetSpotifyService(spotify.NewSpotifyService("cid", "secret", redirectURI, ds.DataDir))
	s.SetSpotifyConfig("cid", "secret", redirectURI)

	return s
}

func TestSpotifyOAuthTransactionIsRandomSingleUseAndBrowserBound(t *testing.T) {
	s := newSpotifyOAuthTestServer(t, "https://example.com/mgmt/spotify/callback")
	stateA, sessionA, err := s.newSpotifyOAuthTransaction("marge-a")
	if err != nil {
		t.Fatal(err)
	}
	stateB, sessionB, err := s.newSpotifyOAuthTransaction("marge-a")
	if err != nil {
		t.Fatal(err)
	}
	if stateA == stateB || sessionA == sessionB || len(stateA) != 64 || len(sessionA) != 64 {
		t.Fatalf("transactions are not independent 32-byte tokens: %q/%q %q/%q", stateA, sessionA, stateB, sessionB)
	}

	if err := s.bootstrapSpotifyOAuthTransaction(stateA, sessionA); !errors.Is(err, errSpotifyOAuthSuperseded) {
		t.Fatalf("older account authorization error = %v, want superseded", err)
	}
	if err := s.bootstrapSpotifyOAuthTransaction(stateB, sessionA); !errors.Is(err, errSpotifyOAuthBootstrap) {
		t.Fatalf("wrong-session bootstrap error = %v", err)
	}
	if err := s.bootstrapSpotifyOAuthTransaction(stateB, sessionB); err != nil {
		t.Fatalf("correct bootstrap failed after mismatch: %v", err)
	}
	if err := s.bootstrapSpotifyOAuthTransaction(stateB, sessionB); !errors.Is(err, errSpotifyOAuthBootstrap) {
		t.Fatalf("bootstrap replay error = %v", err)
	}
	if _, err := s.consumeSpotifyOAuthTransaction(stateB, sessionA); !errors.Is(err, errSpotifyOAuthSession) {
		t.Fatalf("wrong-session consume error = %v", err)
	}
	transaction, err := s.consumeSpotifyOAuthTransaction(stateB, sessionB)
	if err != nil || transaction.MargeAccountID != "marge-a" || transaction.PublicationGeneration != 2 {
		t.Fatalf("correct consume = %+v, %v", transaction, err)
	}
	if _, err := s.consumeSpotifyOAuthTransaction(stateB, sessionB); !errors.Is(err, errSpotifyOAuthStateInvalid) {
		t.Fatalf("state replay error = %v", err)
	}
}

func TestSpotifyOAuthTransactionsAreFencedPerMargeAccount(t *testing.T) {
	s := newSpotifyOAuthTestServer(t, "https://example.com/mgmt/spotify/callback")
	if err := s.ds.SaveDeviceInfo("marge-b", "device-b", &models.ServiceDeviceInfo{DeviceID: "device-b", AccountID: "marge-b"}); err != nil {
		t.Fatal(err)
	}

	stateA, sessionA, err := s.newSpotifyOAuthTransaction("marge-a")
	if err != nil {
		t.Fatal(err)
	}
	stateB, sessionB, err := s.newSpotifyOAuthTransaction("marge-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.bootstrapSpotifyOAuthTransaction(stateA, sessionA); err != nil {
		t.Fatalf("account A transaction was incorrectly superseded: %v", err)
	}
	if err := s.bootstrapSpotifyOAuthTransaction(stateB, sessionB); err != nil {
		t.Fatalf("account B transaction was incorrectly superseded: %v", err)
	}
}

func TestSpotifyOAuthTransactionExpiryAndRandomFailure(t *testing.T) {
	s := newSpotifyOAuthTestServer(t, "https://example.com/mgmt/spotify/callback")
	state, session, err := s.newSpotifyOAuthTransaction("marge-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.bootstrapSpotifyOAuthTransaction(state, session); err != nil {
		t.Fatal(err)
	}
	s.spotifyOAuthMu.Lock()
	transaction := s.spotifyOAuthTransactions[state]
	transaction.ExpiresAt = time.Now().Add(-time.Second)
	s.spotifyOAuthTransactions[state] = transaction
	s.spotifyOAuthMu.Unlock()
	if _, err := s.consumeSpotifyOAuthTransaction(state, session); !errors.Is(err, errSpotifyOAuthStateExpired) {
		t.Fatalf("expired transaction error = %v", err)
	}

	s.spotifyOAuthRandom = failingSpotifyOAuthRandom{}
	if _, _, err := s.newSpotifyOAuthTransaction("marge-a"); err == nil {
		t.Fatal("random-source failure unexpectedly created an OAuth transaction")
	}
}

func TestHandleMgmtSpotifyStartUsesSecureBoundCookie(t *testing.T) {
	s := newSpotifyOAuthTestServer(t, "https://example.com/mgmt/spotify/callback")
	state, session, err := s.newSpotifyOAuthTransaction("marge-a")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/mgmt/spotify/start?state="+state+"&session="+session, nil)
	rec := httptest.NewRecorder()
	s.HandleMgmtSpotifyStart(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("secure bootstrap cookie = %+v", cookies)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("state") != state || location.Query().Get("session") != "" {
		t.Fatalf("provider redirect leaked browser session or lost state: %s", location)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("security headers = %v", rec.Header())
	}
}

func TestValidateSpotifyAuthorizationConfig(t *testing.T) {
	tests := []struct {
		name         string
		clientID     string
		clientSecret string
		redirectURI  string
		wantErr      bool
	}{
		{"HTTPS", "id", "secret", "https://example.com/mgmt/spotify/callback", false},
		{"loopback IPv4 HTTP", "id", "secret", "http://127.0.0.1:8080/mgmt/spotify/callback", false},
		{"loopback IPv6 HTTP", "id", "secret", "http://[::1]:8080/mgmt/spotify/callback", false},
		{"missing client ID", "", "secret", "https://example.com/mgmt/spotify/callback", true},
		{"blank client secret", "id", "  ", "https://example.com/mgmt/spotify/callback", true},
		{"relative redirect", "id", "secret", "/mgmt/spotify/callback", true},
		{"wrong callback path", "id", "secret", "https://example.com/callback", true},
		{"HTTP hostname loopback", "id", "secret", "http://localhost/mgmt/spotify/callback", true},
		{"HTTP private address", "id", "secret", "http://192.168.1.2/mgmt/spotify/callback", true},
		{"query", "id", "secret", "https://example.com/mgmt/spotify/callback?account=x", true},
		{"fragment", "id", "secret", "https://example.com/mgmt/spotify/callback#fragment", true},
		{"userinfo", "id", "secret", "https://user@example.com/mgmt/spotify/callback", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSpotifyAuthorizationConfig(tc.clientID, tc.clientSecret, tc.redirectURI)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateSpotifyAuthorizationConfig() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestReinitSpotifyServiceUsesAuthorizationValidation(t *testing.T) {
	ds := datastore.NewDataStore(t.TempDir())
	s := NewServer(ds, nil, "http://127.0.0.1", false, false, false)
	existing := spotify.NewSpotifyService("old", "old-secret", "http://127.0.0.1/mgmt/spotify/callback", ds.DataDir)
	s.SetSpotifyService(existing)

	s.SetSpotifyConfig("", "secret", "http://127.0.0.1/mgmt/spotify/callback")
	s.ReinitSpotifyService()
	if s.spotifyService != existing {
		t.Fatal("invalid configuration replaced the running Spotify service")
	}

	s.SetSpotifyConfig("new", "new-secret", "https://example.com/mgmt/spotify/callback")
	s.ReinitSpotifyService()
	if s.spotifyService == nil || s.spotifyService != existing {
		t.Fatal("valid configuration replaced the Spotify service and lost its generation fence")
	}
	if got := existing.BuildAuthorizeURL("state"); !strings.Contains(got, "client_id=new") || !strings.Contains(got, url.QueryEscape("https://example.com/mgmt/spotify/callback")) {
		t.Fatalf("reconfigured authorize URL = %q", got)
	}
}

func TestSpotifyOAuthConsumedIntentCannotABAThroughReinit(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	if err := ds.SaveDeviceInfo("marge-a", "device-a", &models.ServiceDeviceInfo{DeviceID: "device-a", AccountID: "marge-a"}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(ds, nil, "http://127.0.0.1", false, false, false)
	s.SetSpotifyConfig("cid", "secret", "https://example.com/mgmt/spotify/callback")
	svc := spotify.NewSpotifyService("cid", "secret", "https://example.com/mgmt/spotify/callback", dir)
	s.SetSpotifyService(svc)

	oldState, oldSession, err := s.newSpotifyOAuthTransaction("marge-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.bootstrapSpotifyOAuthTransaction(oldState, oldSession); err != nil {
		t.Fatal(err)
	}
	oldIntent, err := s.consumeSpotifyOAuthTransaction(oldState, oldSession)
	if err != nil {
		t.Fatal(err)
	}

	// The consumed callback is paused here while configuration is reinitialized
	// and a new account-level intent is issued.
	s.ReinitSpotifyService()
	newState, _, err := s.newSpotifyOAuthTransaction("marge-a")
	if err != nil {
		t.Fatal(err)
	}
	s.spotifyOAuthMu.Lock()
	newIntent := s.spotifyOAuthTransactions[newState]
	s.spotifyOAuthMu.Unlock()
	if newIntent.PublicationGeneration <= oldIntent.PublicationGeneration {
		t.Fatalf("generation did not advance monotonically across reinit: old=%d new=%d", oldIntent.PublicationGeneration, newIntent.PublicationGeneration)
	}
	if s.spotifyOAuthPublicationCurrent(oldIntent) {
		t.Fatal("consumed pre-reinit intent became current again")
	}
}

func TestSpotifyOAuthSupersededAfterProviderExchangeDoesNotStoreCredentials(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	if err := ds.SaveDeviceInfo("marge-a", "device-a", &models.ServiceDeviceInfo{DeviceID: "device-a", AccountID: "marge-a"}); err != nil {
		t.Fatal(err)
	}

	spotifyDir := filepath.Join(dir, "spotify")
	if err := os.MkdirAll(spotifyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	accountsPath := filepath.Join(spotifyDir, "accounts.json")
	before := []byte(`{"existing":{"user_id":"existing","access_token":"keep-access","refresh_token":"keep-refresh","expires_at":4102444800,"bose_secret":"bs-0123456789abcdef0123456789abcdef","generation":4}}`)
	if err := os.WriteFile(accountsPath, before, 0o600); err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = io.WriteString(w, `{"access_token":"superseded-access","refresh_token":"superseded-refresh","expires_in":3600}`)
		case "/me":
			_, _ = io.WriteString(w, `{"id":"superseded-user","display_name":"Superseded User"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	svc := spotify.NewSpotifyService("cid", "secret", "https://example.com/mgmt/spotify/callback", dir)
	svc.SetEndpoints(api.URL+"/token", api.URL)
	if err := svc.Load(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(ds, nil, "http://127.0.0.1", false, false, false)
	s.SetSpotifyService(svc)

	state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-a")
	transaction, err := s.consumeSpotifyOAuthTransaction(state, session)
	if err != nil {
		t.Fatal(err)
	}
	exchangeCompleted := make(chan struct{})
	releaseCommit := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		exchange, err := svc.ExchangeCode("old")
		if err != nil {
			done <- err
			return
		}
		close(exchangeCompleted)
		<-releaseCommit
		_, _, err = s.commitAndPublishSpotifyAuthorization(svc, transaction, exchange)
		done <- err
	}()

	select {
	case <-exchangeCompleted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completed provider exchange")
	}
	if _, _, err := s.newSpotifyOAuthTransaction("marge-a"); err != nil {
		t.Fatal(err)
	}
	close(releaseCommit)
	select {
	case err := <-done:
		if !errors.Is(err, errSpotifyOAuthSuperseded) {
			t.Fatalf("commit error = %v, want superseded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for superseded commit")
	}
	if _, ok := svc.GetLinkedAccount("superseded-user"); ok {
		t.Fatal("superseded exchange committed provider credentials to memory")
	}
	after, err := os.ReadFile(accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("superseded exchange changed accounts.json:\n%s\nwant:\n%s", after, before)
	}
}

func TestSpotifyOAuthCommitAndPublicationShareOwnershipBoundary(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	publicationEntered := make(chan struct{})
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Path {
		case "/setMusicServiceOAuthAccount":
			close(publicationEntered)
			_, _ = io.WriteString(w, `<status>/setMusicServiceOAuthAccount</status>`)
		case "/sources":
			_, _ = io.WriteString(w, `<sources><sourceItem source="SPOTIFY" sourceAccount="linked-user" status="READY">Linked User</sourceItem></sources>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer speaker.Close()
	if err := ds.SaveDeviceInfo("marge-a", "device-a", &models.ServiceDeviceInfo{
		DeviceID:  "device-a",
		AccountID: "marge-a",
		IPAddress: strings.TrimPrefix(speaker.URL, "http://"),
	}); err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = io.WriteString(w, `{"access_token":"linked-access","refresh_token":"linked-refresh","expires_in":3600}`)
		case "/me":
			_, _ = io.WriteString(w, `{"id":"linked-user","display_name":"Linked User"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	svc := spotify.NewSpotifyService("cid", "secret", "https://example.com/mgmt/spotify/callback", dir)
	svc.SetEndpoints(api.URL+"/token", api.URL)
	s := NewServer(ds, nil, "http://127.0.0.1", false, false, false)
	s.SetSpotifyService(svc)
	state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-a")
	transaction, err := s.consumeSpotifyOAuthTransaction(state, session)
	if err != nil {
		t.Fatal(err)
	}
	postStoreEntered := make(chan struct{})
	releasePostStore := make(chan struct{})
	s.spotifyOAuthAfterStore = func() {
		close(postStoreEntered)
		<-releasePostStore
	}
	notificationEntered := make(chan struct{})
	releaseNotification := make(chan struct{})
	s.SetDevicesChangedHook(func() {
		close(notificationEntered)
		<-releaseNotification
	})
	lockEvents := observeSpotifySourceLocks(s)

	type completion struct {
		linked      spotify.LinkedAccount
		publication spotifySourcePublicationResult
		err         error
	}
	completed := make(chan completion, 1)
	go func() {
		linked, publication, err := s.exchangeAndPublishSpotifyAuthorization(svc, transaction, "old")
		completed <- completion{linked: linked, publication: publication, err: err}
	}()
	select {
	case <-postStoreEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for post-store/pre-publication boundary")
	}
	awaitSpotifySourceLockEvent(t, lockEvents, 1, false)
	awaitSpotifySourceLockEvent(t, lockEvents, 1, true)
	select {
	case event := <-lockEvents:
		t.Fatalf("authorization reacquired spotifySourceMu before publication: %+v", event)
	default:
	}
	if s.spotifySourceMu.Mutex.TryLock() {
		s.spotifySourceMu.Mutex.Unlock()
		t.Fatal("spotifySourceMu was not held at the post-store publication boundary")
	}
	if _, ok := svc.GetLinkedAccount("linked-user"); !ok {
		t.Fatal("provider account was not committed before speaker publication")
	}
	accountsPath := filepath.Join(dir, "spotify", "accounts.json")
	accounts, err := os.ReadFile(accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(accounts), "linked-access") {
		t.Fatalf("accounts.json does not contain the committed provider account: %s", accounts)
	}
	select {
	case <-publicationEntered:
		t.Fatal("speaker publication started before the post-store barrier was released")
	default:
	}

	newIntent := make(chan spotifyOAuthTransaction, 1)
	go func() {
		state, _, err := s.newSpotifyOAuthTransaction("marge-a")
		if err != nil {
			newIntent <- spotifyOAuthTransaction{}
			return
		}
		s.spotifyOAuthMu.Lock()
		intent := s.spotifyOAuthTransactions[state]
		s.spotifyOAuthMu.Unlock()
		newIntent <- intent
	}()
	awaitSpotifySourceLockEvent(t, lockEvents, 2, false)
	close(releasePostStore)

	select {
	case <-notificationEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for devices-changed notification")
	}
	awaitSpotifySourceLockEvent(t, lockEvents, 2, true)
	select {
	case intent := <-newIntent:
		if intent.PublicationGeneration <= transaction.PublicationGeneration {
			t.Fatalf("new intent = %+v, old transaction = %+v", intent, transaction)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for newer OAuth intent while notification was blocked")
	}
	if s.spotifyOAuthPublicationCurrent(transaction) {
		t.Fatal("old transaction remained current after the newer intent completed")
	}
	select {
	case result := <-completed:
		t.Fatalf("authorization returned before synchronous notification completed: %+v", result)
	default:
	}
	close(releaseNotification)

	select {
	case result := <-completed:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.linked.UserID != "linked-user" || result.publication.Confirmed != 1 {
			t.Fatalf("authorization completion = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for authorization publication")
	}
}

func TestBridgeSpotifyToMargeReleasesOwnershipBeforeNotification(t *testing.T) {
	s, ds, closeAPI := newSpotifySourceRegistrationFailureServer(t)
	defer closeAPI()
	if err := os.Remove(filepath.Join(ds.AccountDeviceDir("marge-fail", "device-fail"), constants.SourcesFile)); err != nil {
		t.Fatal(err)
	}

	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()
	linked, err := svc.ExchangeCodeAndStore("code")
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := s.newSpotifyOAuthTransaction("marge-fail")
	if err != nil {
		t.Fatal(err)
	}
	s.spotifyOAuthMu.Lock()
	transaction := s.spotifyOAuthTransactions[state]
	s.spotifyOAuthMu.Unlock()

	notificationEntered := make(chan struct{})
	releaseNotification := make(chan struct{})
	s.SetDevicesChangedHook(func() {
		close(notificationEntered)
		<-releaseNotification
	})
	type completion struct {
		publication spotifySourcePublicationResult
		err         error
	}
	completed := make(chan completion, 1)
	go func() {
		publication, err := s.bridgeSpotifyToMarge(transaction, linked)
		completed <- completion{publication: publication, err: err}
	}()

	select {
	case <-notificationEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for devices-changed notification")
	}
	newIntent := make(chan error, 1)
	go func() {
		_, _, err := s.newSpotifyOAuthTransaction("marge-fail")
		newIntent <- err
	}()
	select {
	case err := <-newIntent:
		if err != nil {
			t.Fatalf("newer OAuth intent failed while notification was blocked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for newer OAuth intent while notification was blocked")
	}
	select {
	case result := <-completed:
		t.Fatalf("bridge returned before synchronous notification completed: %+v", result)
	default:
	}
	close(releaseNotification)

	select {
	case result := <-completed:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.publication.Pending != 1 || result.publication.Confirmed != 0 || result.publication.Unverified != 0 {
			t.Fatalf("publication = %+v, want one pending speaker", result.publication)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bridge completion")
	}
}

func TestSpotifyOAuthSameProviderUserPublishesToIndependentMargeAccounts(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	for _, accountID := range []string{"marge-a", "marge-b"} {
		deviceID := "device-" + accountID
		if err := ds.SaveDeviceInfo(accountID, deviceID, &models.ServiceDeviceInfo{DeviceID: deviceID, AccountID: accountID}); err != nil {
			t.Fatal(err)
		}
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-" + r.FormValue("code"), "refresh_token": "refresh", "expires_in": 3600})
		case "/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "shared-spotify-user", "display_name": "Shared User"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	svc := spotify.NewSpotifyService("cid", "secret", "https://example.com/mgmt/spotify/callback", dir)
	svc.SetEndpoints(api.URL+"/token", api.URL)
	s := NewServer(ds, nil, "http://127.0.0.1", false, false, false)
	s.SetSpotifyService(svc)

	stateA, _, err := s.newSpotifyOAuthTransaction("marge-a")
	if err != nil {
		t.Fatal(err)
	}
	stateB, _, err := s.newSpotifyOAuthTransaction("marge-b")
	if err != nil {
		t.Fatal(err)
	}
	s.spotifyOAuthMu.Lock()
	intentA := s.spotifyOAuthTransactions[stateA]
	intentB := s.spotifyOAuthTransactions[stateB]
	s.spotifyOAuthMu.Unlock()

	linkedA, err := svc.ExchangeCodeAndStore("a")
	if err != nil {
		t.Fatal(err)
	}
	linkedB, err := svc.ExchangeCodeAndStore("b")
	if err != nil {
		t.Fatal(err)
	}
	if linkedB.Generation <= linkedA.Generation || linkedA.BoseSecret != linkedB.BoseSecret {
		t.Fatalf("provider-user precondition failed: first=%+v second=%+v", linkedA, linkedB)
	}

	for _, publication := range []struct {
		intent  spotifyOAuthTransaction
		linked  spotify.LinkedAccount
		account string
		device  string
	}{
		{intentA, linkedA, "marge-a", "device-marge-a"},
		{intentB, linkedB, "marge-b", "device-marge-b"},
	} {
		result, err := s.bridgeSpotifyToMarge(publication.intent, publication.linked)
		if err != nil {
			t.Fatalf("publish %s: %v", publication.account, err)
		}
		if result.Pending != 1 || result.Confirmed != 0 || result.Unverified != 0 {
			t.Fatalf("publish %s result = %+v", publication.account, result)
		}
		sources, err := ds.GetConfiguredSources(publication.account, publication.device)
		if err != nil {
			t.Fatal(err)
		}
		if !hasSpotifySourceForUser(sources, "shared-spotify-user") {
			t.Fatalf("%s did not retain its independent Marge binding", publication.account)
		}
	}
}

func newSpotifySourceRegistrationFailureServer(t *testing.T) (*Server, *datastore.DataStore, func()) {
	t.Helper()
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	if err := ds.SaveDeviceInfo("marge-fail", "device-fail", &models.ServiceDeviceInfo{
		DeviceID:  "device-fail",
		AccountID: "marge-fail",
	}); err != nil {
		t.Fatal(err)
	}
	sourcesPath := filepath.Join(ds.AccountDeviceDir("marge-fail", "device-fail"), constants.SourcesFile)
	if err := os.Mkdir(sourcesPath, 0o700); err != nil {
		t.Fatalf("create obstructing Sources.xml directory: %v", err)
	}

	spotifyAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access",
				"refresh_token": "refresh",
				"expires_in":    3600,
			})
		case "/me":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id":           "spotify-user",
				"display_name": "Spotify User",
			})
		default:
			http.NotFound(w, r)
		}
	}))

	svc := spotify.NewSpotifyService("id", "secret", "http://127.0.0.1/mgmt/spotify/callback", dir)
	svc.SetEndpoints(spotifyAPI.URL+"/token", spotifyAPI.URL)
	s := NewServer(ds, nil, "http://127.0.0.1", false, false, false)
	s.SetSpotifyService(svc)
	return s, ds, spotifyAPI.Close
}

func TestBridgeSpotifyToMargeReturnsSourcePersistenceFailure(t *testing.T) {
	s, _, closeAPI := newSpotifySourceRegistrationFailureServer(t)
	defer closeAPI()

	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()
	linked, err := svc.ExchangeCodeAndStore("code")
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := s.newSpotifyOAuthTransaction("marge-fail")
	if err != nil {
		t.Fatal(err)
	}
	s.spotifyOAuthMu.Lock()
	transaction := s.spotifyOAuthTransactions[state]
	s.spotifyOAuthMu.Unlock()
	_, err = s.bridgeSpotifyToMarge(transaction, linked)
	if err == nil {
		t.Fatal("bridgeSpotifyToMarge succeeded despite an unwritable Sources.xml target")
	}
}

func TestBridgeSpotifyToMargeRejectsSupersededAccountPublication(t *testing.T) {
	s, ds, closeAPI := newSpotifySourceRegistrationFailureServer(t)
	defer closeAPI()
	if err := os.Remove(filepath.Join(ds.AccountDeviceDir("marge-fail", "device-fail"), constants.SourcesFile)); err != nil {
		t.Fatal(err)
	}

	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()
	linked, err := svc.ExchangeCodeAndStore("code")
	if err != nil {
		t.Fatal(err)
	}
	oldState, _, err := s.newSpotifyOAuthTransaction("marge-fail")
	if err != nil {
		t.Fatal(err)
	}
	s.spotifyOAuthMu.Lock()
	oldTransaction := s.spotifyOAuthTransactions[oldState]
	s.spotifyOAuthMu.Unlock()
	newState, _, err := s.newSpotifyOAuthTransaction("marge-fail")
	if err != nil {
		t.Fatal(err)
	}
	s.spotifyOAuthMu.Lock()
	newTransaction := s.spotifyOAuthTransactions[newState]
	s.spotifyOAuthMu.Unlock()

	if _, err := s.bridgeSpotifyToMarge(oldTransaction, linked); !errors.Is(err, errSpotifyOAuthSuperseded) {
		t.Fatalf("old publication error = %v, want superseded", err)
	}
	sources, err := ds.GetConfiguredSources("marge-fail", "device-fail")
	if err != nil {
		t.Fatal(err)
	}
	if hasSpotifySourceForUser(sources, linked.UserID) {
		t.Fatal("superseded publication mutated the configured Spotify source")
	}

	publication, err := s.bridgeSpotifyToMarge(newTransaction, linked)
	if err != nil {
		t.Fatal(err)
	}
	if publication.Pending != 1 || publication.Confirmed != 0 || publication.Unverified != 0 {
		t.Fatalf("publication = %+v, want one pending speaker", publication)
	}
	sources, err = ds.GetConfiguredSources("marge-fail", "device-fail")
	if err != nil {
		t.Fatal(err)
	}
	if !hasSpotifySourceForUser(sources, linked.UserID) {
		t.Fatal("current publication did not persist the Spotify source")
	}
}

func TestSpotifyAuthorizationFlowsExposePendingSpeakerPublication(t *testing.T) {
	tests := []struct {
		name     string
		request  func(*Server) (*http.Request, http.Handler)
		wantBody string
	}{
		{
			name: "browser callback",
			request: func(s *Server) (*http.Request, http.Handler) {
				state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-fail")
				req := httptest.NewRequest(http.MethodGet, "/mgmt/spotify/callback?code=code&state="+state, nil)
				addSpotifyOAuthSessionCookie(req, state, session)
				return req, http.HandlerFunc(s.HandleMgmtSpotifyCallback)
			},
			wantBody: "publication remains unverified on 1 speaker",
		},
		{
			name: "app confirm",
			request: func(s *Server) (*http.Request, http.Handler) {
				state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-fail")
				req := httptest.NewRequest(http.MethodPost, "/mgmt/spotify/confirm?code=code&state="+state, nil)
				addSpotifyOAuthSessionCookie(req, state, session)
				return req, http.HandlerFunc(s.HandleMgmtSpotifyConfirm)
			},
			wantBody: `"pending":1`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, ds, closeAPI := newSpotifySourceRegistrationFailureServer(t)
			defer closeAPI()
			if err := os.Remove(filepath.Join(ds.AccountDeviceDir("marge-fail", "device-fail"), constants.SourcesFile)); err != nil {
				t.Fatal(err)
			}
			req, handler := tc.request(s)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestSpotifyAuthorizationPublicationRequiresExactReadyReadback(t *testing.T) {
	tests := []struct {
		name          string
		sourceAccount string
		status        string
		request       func(*Server) (*http.Request, http.Handler)
		wantBody      string
	}{
		{
			name:          "browser UI exact ready confirmation",
			sourceAccount: "spotify-user",
			status:        "READY",
			request: func(s *Server) (*http.Request, http.Handler) {
				state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-fail")
				req := httptest.NewRequest(http.MethodGet, "/mgmt/spotify/callback?code=code&state="+state, nil)
				addSpotifyOAuthSessionCookie(req, state, session)
				return req, http.HandlerFunc(s.HandleMgmtSpotifyCallback)
			},
			wantBody: "stored and published to 1 speaker",
		},
		{
			name:          "browser UI mismatched account remains unverified",
			sourceAccount: "different-user",
			status:        "READY",
			request: func(s *Server) (*http.Request, http.Handler) {
				state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-fail")
				req := httptest.NewRequest(http.MethodGet, "/mgmt/spotify/callback?code=code&state="+state, nil)
				addSpotifyOAuthSessionCookie(req, state, session)
				return req, http.HandlerFunc(s.HandleMgmtSpotifyCallback)
			},
			wantBody: "publication remains unverified on 1 speaker",
		},
		{
			name:          "JSON non-ready source is unverified",
			sourceAccount: "spotify-user",
			status:        "UNAVAILABLE",
			request: func(s *Server) (*http.Request, http.Handler) {
				state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-fail")
				req := httptest.NewRequest(http.MethodPost, "/mgmt/spotify/confirm?code=code&state="+state, nil)
				addSpotifyOAuthSessionCookie(req, state, session)
				return req, http.HandlerFunc(s.HandleMgmtSpotifyConfirm)
			},
			wantBody: `"publication":{"confirmed":0,"pending":0,"unverified":1}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/setMusicServiceOAuthAccount":
					w.Header().Set("Content-Type", "application/xml")
					_, _ = io.WriteString(w, `<status>/setMusicServiceOAuthAccount</status>`)
				case "/sources":
					w.Header().Set("Content-Type", "application/xml")
					_, _ = io.WriteString(w, `<sources><sourceItem source="SPOTIFY" sourceAccount="`+tc.sourceAccount+`" status="`+tc.status+`">Spotify</sourceItem></sources>`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer speaker.Close()

			s, ds, closeAPI := newSpotifySourceRegistrationFailureServer(t)
			defer closeAPI()
			if err := os.Remove(filepath.Join(ds.AccountDeviceDir("marge-fail", "device-fail"), constants.SourcesFile)); err != nil {
				t.Fatal(err)
			}
			device, err := ds.GetDeviceInfo("marge-fail", "device-fail")
			if err != nil {
				t.Fatal(err)
			}
			device.IPAddress = strings.TrimPrefix(speaker.URL, "http://")
			if err := ds.SaveDeviceInfo("marge-fail", "device-fail", device); err != nil {
				t.Fatal(err)
			}

			req, handler := tc.request(s)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestSpotifyAuthorizationFlowsReportSourceRegistrationFailure(t *testing.T) {
	tests := []struct {
		name       string
		request    func(*Server) (*http.Request, http.Handler)
		wantStatus int
		wantBody   string
	}{
		{
			name: "browser callback",
			request: func(s *Server) (*http.Request, http.Handler) {
				state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-fail")
				req := httptest.NewRequest(http.MethodGet, "/mgmt/spotify/callback?code=code&state="+state, nil)
				addSpotifyOAuthSessionCookie(req, state, session)
				return req, http.HandlerFunc(s.HandleMgmtSpotifyCallback)
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "source registration failed",
		},
		{
			name: "app confirm",
			request: func(s *Server) (*http.Request, http.Handler) {
				state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-fail")
				req := httptest.NewRequest(http.MethodPost, "/mgmt/spotify/confirm?code=code&state="+state, nil)
				addSpotifyOAuthSessionCookie(req, state, session)
				return req, http.HandlerFunc(s.HandleMgmtSpotifyConfirm)
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "source registration failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, closeAPI := newSpotifySourceRegistrationFailureServer(t)
			defer closeAPI()
			req, handler := tc.request(s)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}
