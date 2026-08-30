package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
	"github.com/gesellix/bose-soundtouch/pkg/service/spotify"
	"github.com/go-chi/chi/v5"
)

const (
	spotifyUserASecret = "bs-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	spotifyUserBSecret = "bs-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func setPrivateRemoteAddr(req *http.Request) {
	req.RemoteAddr = "192.168.10.20:54321"
}

func spotifyConfiguredSourceForTest(userID, secret string) models.ConfiguredSource {
	source := models.ConfiguredSource{
		DisplayName:      userID,
		Secret:           secret,
		SecretType:       constants.CredentialTypeTokenV3,
		SourceProviderID: "15",
	}
	source.SourceKey.Type = constants.ProviderSpotify
	source.SourceKey.Account = userID
	return source
}

func saveSpotifyBindingForTest(t *testing.T, ds *datastore.DataStore, accountID, deviceID, userID, secret string) {
	t.Helper()
	if err := ds.SaveDeviceInfo(accountID, deviceID, &models.ServiceDeviceInfo{
		DeviceID:  deviceID,
		AccountID: accountID,
		Name:      deviceID,
		IPAddress: "192.168.10.20",
	}); err != nil {
		t.Fatalf("SaveDeviceInfo: %v", err)
	}
	if err := ds.SaveConfiguredSources(accountID, deviceID, []models.ConfiguredSource{
		spotifyConfiguredSourceForTest(userID, secret),
	}); err != nil {
		t.Fatalf("SaveConfiguredSources: %v", err)
	}
}

func spotifyServiceForHandlerTest(t *testing.T, dataDir string) *spotify.Service {
	t.Helper()
	spotifyDir := filepath.Join(dataDir, "spotify")
	if err := os.MkdirAll(spotifyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	accounts := map[string]map[string]any{
		"user-a": {
			"user_id":       "user-a",
			"display_name":  "User A",
			"access_token":  "token-a",
			"refresh_token": "refresh-a",
			"expires_at":    time.Now().Add(time.Hour).Unix(),
			"bose_secret":   spotifyUserASecret,
		},
		"user-b": {
			"user_id":       "user-b",
			"display_name":  "User B",
			"access_token":  "token-b",
			"refresh_token": "refresh-b",
			"expires_at":    time.Now().Add(time.Hour).Unix(),
			"bose_secret":   spotifyUserBSecret,
		},
	}
	data, err := json.Marshal(accounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spotifyDir, "accounts.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := spotify.NewSpotifyService("client", "secret", "http://localhost/callback", dataDir)
	if err := svc.Load(); err != nil {
		t.Fatalf("load Spotify service: %v", err)
	}
	return svc
}

func TestBindingFromSourcesRequiresOneExactSpotifyMapping(t *testing.T) {
	nonSpotify := spotifyConfiguredSourceForTest("wrong-user", "wrong-secret")
	nonSpotify.SourceKey.Type = constants.ProviderAmazon
	nonSpotify.SourceProviderID = "20"
	exact := spotifyConfiguredSourceForTest("user-a", spotifyUserASecret)

	binding, err := bindingFromSources("marge-a", "device-a", []models.ConfiguredSource{nonSpotify, exact})
	if err != nil {
		t.Fatal(err)
	}
	if binding.MargeAccountID != "marge-a" || binding.DeviceID != "device-a" || binding.UserID != "user-a" || binding.Secret != spotifyUserASecret {
		t.Fatalf("binding = %+v", binding)
	}

	if _, err := bindingFromSources("marge-a", "device-a", []models.ConfiguredSource{nonSpotify}); !errors.Is(err, errSpotifyBindingNotFound) {
		t.Fatalf("wrong-type error = %v, want not found", err)
	}
	if _, err := bindingFromSources("marge-a", "device-a", []models.ConfiguredSource{exact, spotifyConfiguredSourceForTest("user-b", spotifyUserBSecret)}); !errors.Is(err, errSpotifyBindingAmbiguous) {
		t.Fatalf("multi-binding error = %v, want ambiguous", err)
	}
}

func TestSpotifyBrokerResolvesExactDeviceAndMargeAccountBindings(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	s := NewServer(ds, nil, "http://localhost", false, false, false)
	s.SetSpotifyService(spotifyServiceForHandlerTest(t, dir))
	saveSpotifyBindingForTest(t, ds, "marge-a", "device-a", "user-a", spotifyUserASecret)
	saveSpotifyBindingForTest(t, ds, "marge-b", "device-b", "user-b", spotifyUserBSecret)

	router := chi.NewRouter()
	router.Post("/oauth/device/{deviceID}/music/musicprovider/{sourceID}/token/cs3", s.HandleBoseToken)

	assertTokenResponse := func(path, secret, wantToken string) {
		t.Helper()
		body := strings.NewReader(`{"grant_type":"refresh_token","refresh_token":"` + secret + `"}`)
		req := httptest.NewRequest(http.MethodPost, path, body)
		setPrivateRemoteAddr(req)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", path, rec.Code, rec.Body.String())
		}
		var response map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response["access_token"] != wantToken {
			t.Fatalf("%s access_token = %v, want %s", path, response["access_token"], wantToken)
		}
	}

	assertTokenResponse("/oauth/device/device-a/music/musicprovider/15/token/cs3", spotifyUserASecret, "token-a")
}

func TestSpotifyBrokerRejectsMissingWrongAndAmbiguousMappingsWithoutLoggingSecret(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	s := NewServer(ds, nil, "http://localhost", false, false, false)
	s.SetSpotifyService(spotifyServiceForHandlerTest(t, dir))
	saveSpotifyBindingForTest(t, ds, "marge-a", "device-a", "user-a", spotifyUserASecret)

	ambiguousSources := []models.ConfiguredSource{
		spotifyConfiguredSourceForTest("user-a", spotifyUserASecret),
		spotifyConfiguredSourceForTest("user-b", spotifyUserBSecret),
	}
	if err := ds.SaveDeviceInfo("marge-amb", "device-amb", &models.ServiceDeviceInfo{DeviceID: "device-amb", AccountID: "marge-amb"}); err != nil {
		t.Fatal(err)
	}
	if err := ds.SaveConfiguredSources("marge-amb", "device-amb", ambiguousSources); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Post("/oauth/device/{deviceID}/music/musicprovider/{sourceID}/token/cs3", s.HandleBoseToken)

	const suppliedSecret = "supplied-secret-must-not-appear"
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	tests := []struct {
		name   string
		path   string
		secret string
		status int
	}{
		{"missing device", "/oauth/device/missing/music/musicprovider/15/token/cs3", suppliedSecret, http.StatusConflict},
		{"wrong secret", "/oauth/device/device-a/music/musicprovider/15/token/cs3", suppliedSecret, http.StatusUnauthorized},
		{"ambiguous device sources", "/oauth/device/device-amb/music/musicprovider/15/token/cs3", suppliedSecret, http.StatusConflict},
		{"wrong client address", "/oauth/device/device-a/music/musicprovider/15/token/cs3", spotifyUserASecret, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(`{"grant_type":"refresh_token","refresh_token":"` + tc.secret + `"}`)
			req := httptest.NewRequest(http.MethodPost, tc.path, body)
			if tc.name == "wrong client address" {
				req.RemoteAddr = "192.168.10.21:54321"
			} else {
				setPrivateRemoteAddr(req)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
	if strings.Contains(logs.String(), suppliedSecret) {
		t.Fatalf("broker logs exposed supplied secret: %s", logs.String())
	}
}

func TestBoseAccountTokenNeverExportsProviderTokens(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	s := NewServer(ds, nil, "http://localhost", false, false, false)
	s.SetSpotifyService(spotifyServiceForHandlerTest(t, dir))
	saveSpotifyBindingForTest(t, ds, "marge-a", "device-a", "user-a", spotifyUserASecret)

	router := chi.NewRouter()
	router.Post("/oauth/account/{account}/music/musicprovider/{sourceID}/token/cs", s.HandleBoseAccountToken)
	path := "/oauth/account/marge-a/music/musicprovider/15/token/cs"
	body := `{"grant_type":"refresh_token","refresh_token":"` + spotifyUserASecret + `"}`
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	setPrivateRemoteAddr(req)
	req.Header.Set("Authorization", "mock-token-marge-a")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone || strings.Contains(rec.Body.String(), "token-a") {
		t.Fatalf("status = %d body=%q, want non-secret 410", rec.Code, rec.Body.String())
	}
}

func TestSpotifyDeviceBrokerRejectsOversizedCredentialBody(t *testing.T) {
	dir := t.TempDir()
	ds := datastore.NewDataStore(dir)
	s := NewServer(ds, nil, "http://localhost", false, false, false)
	s.SetSpotifyService(spotifyServiceForHandlerTest(t, dir))
	saveSpotifyBindingForTest(t, ds, "marge-a", "device-a", "user-a", spotifyUserASecret)

	router := chi.NewRouter()
	router.Post("/oauth/device/{deviceID}/music/musicprovider/{sourceID}/token/cs3", s.HandleBoseToken)
	body := `{"grant_type":"refresh_token","refresh_token":"` + strings.Repeat("x", (64<<10)+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth/device/device-a/music/musicprovider/15/token/cs3", strings.NewReader(body))
	setPrivateRemoteAddr(req)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestSpotifyBrokerRejectsPublicClients(t *testing.T) {
	s := NewServer(datastore.NewDataStore(t.TempDir()), nil, "http://localhost", false, false, false)
	router := chi.NewRouter()
	router.Post("/oauth/device/{deviceID}/music/musicprovider/{sourceID}/token/cs3", s.HandleBoseToken)
	router.Post("/oauth/account/{account}/music/musicprovider/{sourceID}/token/cs", s.HandleBoseAccountToken)

	for _, path := range []string{
		"/oauth/device/device-a/music/musicprovider/15/token/cs3",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"grant_type":"refresh_token","refresh_token":"secret"}`))
			req.RemoteAddr = "203.0.113.10:54321"
			req.Header.Set("Authorization", "mock-token-marge-a")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSpotifyBindingForDeviceRejectsCrossAccountAmbiguity(t *testing.T) {
	ds := datastore.NewDataStore(t.TempDir())
	s := NewServer(ds, nil, "http://localhost", false, false, false)
	saveSpotifyBindingForTest(t, ds, "marge-a", "shared-device", "user-a", spotifyUserASecret)
	saveSpotifyBindingForTest(t, ds, "marge-b", "shared-device", "user-b", spotifyUserBSecret)

	if _, err := s.spotifyBindingForDevice("shared-device"); !errors.Is(err, errSpotifyBindingAmbiguous) {
		t.Fatalf("spotifyBindingForDevice error = %v, want ambiguous duplicate ownership", err)
	}
}
