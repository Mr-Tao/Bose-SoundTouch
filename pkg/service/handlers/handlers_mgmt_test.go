package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
	"github.com/gesellix/bose-soundtouch/pkg/service/spotify"
	"github.com/go-chi/chi/v5"
)

func prepareSpotifyOAuthBrowserSession(t *testing.T, s *Server, accountID string) (state, session string) {
	t.Helper()

	state, session, err := s.newSpotifyOAuthTransaction(accountID)
	if err != nil {
		t.Fatalf("create Spotify OAuth transaction: %v", err)
	}
	if err := s.bootstrapSpotifyOAuthTransaction(state, session); err != nil {
		t.Fatalf("bootstrap Spotify OAuth transaction: %v", err)
	}

	return state, session
}

func addSpotifyOAuthSessionCookie(req *http.Request, state, session string) {
	req.AddCookie(&http.Cookie{Name: spotifyOAuthCookieName(state), Value: session})
}

func TestHandleMgmtSpotifyInit(t *testing.T) {
	ds := datastore.NewDataStore(t.TempDir())
	if err := ds.SaveDeviceInfo("marge-a", "device-a", &models.ServiceDeviceInfo{DeviceID: "device-a", AccountID: "marge-a"}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(ds, nil, "http://localhost", false, false, false)

	t.Run("POST - No spotify service configured", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/mgmt/spotify/init", nil)
		w := httptest.NewRecorder()
		s.HandleMgmtSpotifyInit(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
	})

	// With spotify service
	svc := spotify.NewSpotifyService("cid", "secret", "http://127.0.0.1/mgmt/spotify/callback", t.TempDir())
	s.SetSpotifyService(svc)
	s.SetSpotifyConfig("cid", "secret", "http://127.0.0.1/mgmt/spotify/callback")

	for _, tc := range []struct {
		name, clientID, clientSecret, redirectURI string
	}{
		{"missing client ID", "", "secret", "http://127.0.0.1/mgmt/spotify/callback"},
		{"missing client secret", "cid", "", "http://127.0.0.1/mgmt/spotify/callback"},
		{"invalid redirect", "cid", "secret", "http://localhost/callback"},
	} {
		t.Run("POST - Preflight rejects "+tc.name, func(t *testing.T) {
			s.SetSpotifyConfig(tc.clientID, tc.clientSecret, tc.redirectURI)
			req := httptest.NewRequest(http.MethodPost, "/mgmt/spotify/init?account=marge-a", nil)
			rec := httptest.NewRecorder()
			s.HandleMgmtSpotifyInit(rec, req)
			if rec.Code != http.StatusPreconditionFailed {
				t.Fatalf("status = %d, want 412", rec.Code)
			}
		})
	}
	s.SetSpotifyConfig("cid", "secret", "http://127.0.0.1/mgmt/spotify/callback")

	t.Run("POST - Success", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/mgmt/spotify/init?account=marge-a", nil)
		w := httptest.NewRecorder()
		s.HandleMgmtSpotifyInit(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		var resp map[string]string
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		redirect, err := url.Parse(resp["redirectUrl"])
		if err != nil {
			t.Fatal(err)
		}
		state := redirect.Query().Get("state")
		if len(state) != 64 || state == "marge-a" {
			t.Fatalf("OAuth state = %q, want opaque 32-byte transaction token", state)
		}
		session := redirect.Query().Get("session")
		if len(session) != 64 {
			t.Fatalf("OAuth browser session length = %d, want 64", len(session))
		}
		if redirect.Path != "/mgmt/spotify/start" {
			t.Fatalf("bootstrap path = %q, want /mgmt/spotify/start", redirect.Path)
		}
		if redirect.Query().Get("client_id") != "" {
			t.Fatalf("authenticated init exposed a provider URL instead of a local bootstrap: %s", redirect)
		}

		startReq := httptest.NewRequest(http.MethodGet, redirect.String(), nil)
		startRec := httptest.NewRecorder()
		s.HandleMgmtSpotifyStart(startRec, startReq)
		if startRec.Code != http.StatusSeeOther {
			t.Fatalf("bootstrap status = %d, want 303: %s", startRec.Code, startRec.Body.String())
		}
		providerURL, err := url.Parse(startRec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if providerURL.Query().Get("client_id") != "cid" || providerURL.Query().Get("state") != state {
			t.Fatalf("provider redirect = %s, want client_id and opaque state", providerURL)
		}
		if startRec.Header().Get("Cache-Control") != "no-store" || startRec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("bootstrap security headers = %v", startRec.Header())
		}
		cookies := startRec.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != spotifyOAuthCookieName(state) || cookies[0].Value != session {
			t.Fatalf("bootstrap cookie = %+v", cookies)
		}
		if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Secure {
			t.Fatalf("bootstrap cookie flags = %+v", cookies[0])
		}

		replayRec := httptest.NewRecorder()
		s.HandleMgmtSpotifyStart(replayRec, startReq)
		if replayRec.Code != http.StatusBadRequest {
			t.Fatalf("replayed bootstrap status = %d, want 400", replayRec.Code)
		}
	})

	for _, account := range []string{"", "default", "missing"} {
		t.Run("reject account "+account, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mgmt/spotify/init?account="+url.QueryEscape(account), nil)
			rec := httptest.NewRecorder()
			s.HandleMgmtSpotifyInit(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestHandleMgmtSpotifyAccounts(t *testing.T) {
	s := NewServer(nil, nil, "http://localhost", false, false, false)
	svc := spotify.NewSpotifyService("cid", "secret", "http://localhost/cb", t.TempDir())
	s.SetSpotifyService(svc)

	req := httptest.NewRequest("GET", "/mgmt/spotify/accounts", nil)
	w := httptest.NewRecorder()
	s.HandleMgmtSpotifyAccounts(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string][]spotify.AccountSummary
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if len(resp["accounts"]) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(resp["accounts"]))
	}
}

func TestHandleMgmtSpotifyTokenIsNonSecretGoneTombstone(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(datastore.NewDataStore(dir), nil, "http://localhost", false, false, false)
	s.SetSpotifyService(spotifyServiceForHandlerTest(t, dir))

	req := httptest.NewRequest(http.MethodGet, "/mgmt/spotify/token?account=user-a", nil)
	rec := httptest.NewRecorder()
	s.HandleMgmtSpotifyToken(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rec.Code)
	}
	if rec.Header().Get("Deprecation") != "true" {
		t.Fatalf("Deprecation = %q, want true", rec.Header().Get("Deprecation"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if warning := rec.Header().Get("Warning"); !strings.Contains(warning, "token export was removed") {
		t.Fatalf("Warning = %q", warning)
	}
	for _, secret := range []string{"token-a", "refresh-a", spotifyUserASecret} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("tombstone exposed secret %q: %s", secret, rec.Body.String())
		}
	}
}

func TestHandleMgmtListSpeakers(t *testing.T) {
	tmpDir := t.TempDir()
	ds := datastore.NewDataStore(tmpDir)
	_, s := setupRouter("http://localhost:8000", ds)

	req := httptest.NewRequest("GET", "/mgmt/accounts/default/speakers", nil)
	w := httptest.NewRecorder()
	s.HandleMgmtListSpeakers(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if _, ok := resp["speakers"]; !ok {
		t.Error("expected 'speakers' in response")
	}
}

func TestHandleMgmtSpotifyCallback(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	for _, account := range []string{"marge-a", "marge-b"} {
		if err := ds.SaveDeviceInfo(account, "device-"+account, &models.ServiceDeviceInfo{DeviceID: "device-" + account, AccountID: account}); err != nil {
			t.Fatal(err)
		}
	}
	s := NewServer(ds, nil, "http://localhost", false, false, false)
	svc := spotify.NewSpotifyService("cid", "secret", "http://localhost/cb", t.TempDir())
	s.SetSpotifyService(svc)

	// Mock Spotify token and profile endpoints
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "at",
			"refresh_token": "rt",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	profileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":           "user123",
			"display_name": "Test User",
		})
	}))
	defer profileServer.Close()

	svc.SetEndpoints(tokenServer.URL, profileServer.URL)

	t.Run("Mismatched state", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/mgmt/spotify/callback?code=code&state=wrong", nil)
		rec := httptest.NewRecorder()
		s.HandleMgmtSpotifyCallback(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("Missing code", func(t *testing.T) {
		state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-a")
		req := httptest.NewRequest("GET", "/mgmt/spotify/callback?state="+state, nil)
		addSpotifyOAuthSessionCookie(req, state, session)
		w := httptest.NewRecorder()
		s.HandleMgmtSpotifyCallback(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "Missing authorization code") {
			t.Errorf("expected missing code error message, got %s", w.Body.String())
		}
	})

	t.Run("Spotify error", func(t *testing.T) {
		state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-a")
		req := httptest.NewRequest("GET", "/mgmt/spotify/callback?error=access_denied&state="+state, nil)
		addSpotifyOAuthSessionCookie(req, state, session)
		w := httptest.NewRecorder()
		s.HandleMgmtSpotifyCallback(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "access_denied") {
			t.Errorf("expected access_denied error message, got %s", w.Body.String())
		}
	})

	t.Run("Success uses transaction account and ignores query account", func(t *testing.T) {
		state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-a")
		req := httptest.NewRequest(http.MethodGet, "/mgmt/spotify/callback?code=code&state="+state+"&account=marge-b", nil)
		addSpotifyOAuthSessionCookie(req, state, session)
		rec := httptest.NewRecorder()
		s.HandleMgmtSpotifyCallback(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}

		sourcesA, err := ds.GetConfiguredSources("marge-a", "device-marge-a")
		if err != nil {
			t.Fatal(err)
		}
		if !hasSpotifySourceForUser(sourcesA, "user123") {
			t.Fatalf("transaction account lacks linked Spotify source: %+v", sourcesA)
		}
		sourcesB, err := ds.GetConfiguredSources("marge-b", "device-marge-b")
		if err != nil {
			t.Fatal(err)
		}
		if hasSpotifySourceForUser(sourcesB, "user123") {
			t.Fatalf("query account received Spotify source: %+v", sourcesB)
		}

		replay := httptest.NewRecorder()
		s.HandleMgmtSpotifyCallback(replay, req)
		if replay.Code != http.StatusBadRequest {
			t.Fatalf("replayed callback status = %d, want 400", replay.Code)
		}
	})
}

func TestHandleMgmtSpotifyConfirm(t *testing.T) {
	ds := datastore.NewDataStore(t.TempDir())
	if err := ds.SaveDeviceInfo("marge-a", "device-a", &models.ServiceDeviceInfo{DeviceID: "device-a", AccountID: "marge-a"}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(ds, nil, "http://localhost", false, false, false)
	svc := spotify.NewSpotifyService("cid", "secret", "http://localhost/cb", t.TempDir())
	s.SetSpotifyService(svc)

	t.Run("Missing code", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/mgmt/spotify/confirm", nil)
		w := httptest.NewRecorder()
		s.HandleMgmtSpotifyConfirm(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("Mismatched state", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mgmt/spotify/confirm?code=code&state=wrong", nil)
		rec := httptest.NewRecorder()
		s.HandleMgmtSpotifyConfirm(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("Expired state", func(t *testing.T) {
		state, session := prepareSpotifyOAuthBrowserSession(t, s, "marge-a")
		s.spotifyOAuthMu.Lock()
		transaction := s.spotifyOAuthTransactions[state]
		transaction.ExpiresAt = time.Now().Add(-time.Second)
		s.spotifyOAuthTransactions[state] = transaction
		s.spotifyOAuthMu.Unlock()
		req := httptest.NewRequest(http.MethodPost, "/mgmt/spotify/confirm?code=code&state="+state, nil)
		addSpotifyOAuthSessionCookie(req, state, session)
		rec := httptest.NewRecorder()
		s.HandleMgmtSpotifyConfirm(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}

func hasSpotifySourceForUser(sources []models.ConfiguredSource, userID string) bool {
	for _, source := range sources {
		if isSpotifyConfiguredSource(source) && source.SourceKey.Account == userID && source.Secret != "" && source.SecretType == constants.CredentialTypeTokenV3 {
			return true
		}
	}
	return false
}

func TestHandleMgmtDeviceEvents(t *testing.T) {
	tmpDir := t.TempDir()
	ds := datastore.NewDataStore(tmpDir)
	_, s := setupRouter("http://localhost:8000", ds)

	r := chi.NewRouter()
	r.Get("/mgmt/devices/{deviceId}/events", s.HandleMgmtDeviceEvents)

	req := httptest.NewRequest("GET", "/mgmt/devices/device123/events", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if _, ok := resp["events"]; !ok {
		t.Error("expected 'events' in response")
	}
}

func TestBasicAuthMgmt(t *testing.T) {
	s := NewServer(nil, nil, "http://localhost", false, false, false)
	s.SetMgmtConfig("admin", "secret123")

	handler := s.BasicAuthMgmt()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))

	t.Run("Valid credentials", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/mgmt/test", nil)
		req.SetBasicAuth("admin", "secret123")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
		}
		if rr.Body.String() != "OK" {
			t.Errorf("expected body 'OK', got %q", rr.Body.String())
		}
	})

	t.Run("Wrong username", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/mgmt/test", nil)
		req.SetBasicAuth("wrong", "secret123")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
		}
		if rr.Header().Get("WWW-Authenticate") == "" {
			t.Error("expected WWW-Authenticate header to be set")
		}
	})

	t.Run("Wrong password", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/mgmt/test", nil)
		req.SetBasicAuth("admin", "wrongpass")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
		}
	})

	t.Run("Missing auth header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/mgmt/test", nil)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
		}
	})

	t.Run("Empty credentials", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/mgmt/test", nil)
		req.SetBasicAuth("", "")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
		}
	})
}

// TestBasicAuthAdmin covers the #419 admin-area gate: unlike BasicAuthMgmt
// (credentials captured once at router-setup time), BasicAuthAdmin must
// read the live AdminAreaAuth mode and credentials on every request, so a
// live toggle via the Settings UI takes effect without a restart.
func TestBasicAuthAdmin(t *testing.T) {
	s := NewServer(nil, nil, "http://localhost", false, false, false)
	s.SetMgmtConfig("admin", "secret123")

	handler := s.BasicAuthAdmin()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))

	t.Run("Unset mode passes through unauthenticated (today's default)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status %d for unset mode, got %d", http.StatusOK, rr.Code)
		}
	})

	t.Run("Disabled mode passes through unauthenticated", func(t *testing.T) {
		s.SetAdminAreaAuth("disabled")
		defer s.SetAdminAreaAuth("")

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status %d for disabled mode, got %d", http.StatusOK, rr.Code)
		}
	})

	t.Run("Enabled mode requires valid credentials", func(t *testing.T) {
		s.SetAdminAreaAuth("enabled")
		defer s.SetAdminAreaAuth("")

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		req.SetBasicAuth("admin", "secret123")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status %d with valid credentials, got %d", http.StatusOK, rr.Code)
		}
	})

	t.Run("Enabled mode rejects missing credentials", func(t *testing.T) {
		s.SetAdminAreaAuth("enabled")
		defer s.SetAdminAreaAuth("")

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d with no credentials, got %d", http.StatusUnauthorized, rr.Code)
		}
		if rr.Header().Get("WWW-Authenticate") == "" {
			t.Error("expected WWW-Authenticate header to be set")
		}
	})

	t.Run("Enabled mode rejects wrong credentials", func(t *testing.T) {
		s.SetAdminAreaAuth("enabled")
		defer s.SetAdminAreaAuth("")

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		req.SetBasicAuth("admin", "wrongpass")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d with wrong credentials, got %d", http.StatusUnauthorized, rr.Code)
		}
	})

	t.Run("Toggling mode live changes behavior without rebuilding the handler", func(t *testing.T) {
		// The whole point of reading s.adminAreaAuth per-request rather than
		// capturing it once: the same handler value must reflect a live change.
		s.SetAdminAreaAuth("")
		reqOpen := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rrOpen := httptest.NewRecorder()
		handler.ServeHTTP(rrOpen, reqOpen)

		if rrOpen.Code != http.StatusOK {
			t.Fatalf("expected open access before toggling, got %d", rrOpen.Code)
		}

		s.SetAdminAreaAuth("enabled")
		defer s.SetAdminAreaAuth("")

		reqGated := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rrGated := httptest.NewRecorder()
		handler.ServeHTTP(rrGated, reqGated)

		if rrGated.Code != http.StatusUnauthorized {
			t.Errorf("expected the SAME handler to enforce auth immediately after toggling, got %d", rrGated.Code)
		}
	})
}
