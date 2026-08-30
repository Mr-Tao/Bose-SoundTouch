package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

type stubDirectorySyncCloser struct {
	syncErr     error
	closeErr    error
	syncCalled  bool
	closeCalled bool
}

func (s *stubDirectorySyncCloser) Sync() error {
	s.syncCalled = true
	return s.syncErr
}

func (s *stubDirectorySyncCloser) Close() error {
	s.closeCalled = true
	return s.closeErr
}

func TestBuildAuthorizeURL(t *testing.T) {
	svc := NewSpotifyService("test-client-id", "test-secret", "http://localhost/callback", t.TempDir())

	state := "test-state"
	url := svc.BuildAuthorizeURL(state)

	if !strings.Contains(url, "client_id=test-client-id") {
		t.Errorf("URL should contain client_id, got: %s", url)
	}
	if !strings.Contains(url, "redirect_uri=") {
		t.Errorf("URL should contain redirect_uri, got: %s", url)
	}
	if !strings.Contains(url, "scope=") {
		t.Errorf("URL should contain scope, got: %s", url)
	}
	if !strings.Contains(url, "response_type=code") {
		t.Errorf("URL should contain response_type=code, got: %s", url)
	}
	if !strings.Contains(url, "state=test-state") {
		t.Errorf("URL should contain state=test-state, got: %s", url)
	}
	if !strings.HasPrefix(url, SpotifyAuthorizeURL) {
		t.Errorf("URL should start with %s, got: %s", SpotifyAuthorizeURL, url)
	}
}

func TestReconfigurePreservesAccountsAndAdvancesConfigGeneration(t *testing.T) {
	svc := NewSpotifyService("old-client", "old-secret", "http://localhost/old", t.TempDir())
	svc.accounts["user"] = &Account{UserID: "user", Generation: 4}
	before := cloneAccounts(svc.accounts)
	beforeGeneration := svc.configGeneration

	svc.Reconfigure("new-client", "new-secret", "http://localhost/new")

	if !reflect.DeepEqual(svc.accounts, before) {
		t.Fatalf("accounts changed during reconfigure: got %#v want %#v", svc.accounts, before)
	}
	if svc.configGeneration != beforeGeneration+1 {
		t.Fatalf("config generation = %d, want %d", svc.configGeneration, beforeGeneration+1)
	}
	authorizeURL := svc.BuildAuthorizeURL("")
	if !strings.Contains(authorizeURL, "client_id=new-client") || !strings.Contains(authorizeURL, "redirect_uri=http%3A%2F%2Flocalhost%2Fnew") {
		t.Fatalf("authorize URL does not use reconfigured client: %s", authorizeURL)
	}
}

func TestGetAccountsStripsTokens(t *testing.T) {
	svc := NewSpotifyService("cid", "csecret", "http://localhost/cb", t.TempDir())

	// Manually add an account with tokens
	svc.mu.Lock()
	svc.accounts["user1"] = &Account{
		UserID:       "user1",
		DisplayName:  "Test User",
		Email:        "test@example.com",
		AccessToken:  "secret-access-token",
		RefreshToken: "secret-refresh-token",
		BoseSecret:   "bs-0123456789abcdef0123456789abcdef",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	svc.mu.Unlock()

	accounts := svc.GetAccounts()

	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}

	encoded, err := json.Marshal(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "access_token") || strings.Contains(string(encoded), "refresh_token") || strings.Contains(string(encoded), "bose_secret") {
		t.Fatalf("account summary exposed a secret field: %s", encoded)
	}
	if accounts[0].UserID != "user1" {
		t.Errorf("UserID should be preserved, got: %s", accounts[0].UserID)
	}
	if accounts[0].DisplayName != "Test User" {
		t.Errorf("DisplayName should be preserved, got: %s", accounts[0].DisplayName)
	}
}

func TestGetFreshTokenRefreshesExpired(t *testing.T) {
	// Set up a mock Spotify token endpoint
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type=refresh_token, got %s", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "my-refresh-token" {
			t.Errorf("expected refresh_token=my-refresh-token, got %s", r.Form.Get("refresh_token"))
		}

		// Verify Basic Auth
		user, pass, ok := r.BasicAuth()
		if !ok || user != "cid" || pass != "csecret" {
			t.Errorf("expected Basic Auth cid:csecret, got %s:%s (ok=%v)", user, pass, ok)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "new-access-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "new-refresh-token",
		})
	}))
	defer tokenServer.Close()

	svc := NewSpotifyService("cid", "csecret", "http://localhost/cb", t.TempDir())

	// Override the token URL for testing
	svc.tokenURL = tokenServer.URL

	// Add an account with an expired token
	svc.mu.Lock()
	svc.accounts["user1"] = &Account{
		UserID:       "user1",
		DisplayName:  "Test User",
		AccessToken:  "old-expired-token",
		RefreshToken: "my-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(), // expired
		Generation:   3,
	}
	svc.mu.Unlock()

	accessToken, username, err := svc.GetFreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if accessToken != "new-access-token" {
		t.Errorf("expected new-access-token, got %s", accessToken)
	}
	if username != "user1" {
		t.Errorf("expected user1, got %s", username)
	}

	// Verify the account was updated
	svc.mu.RLock()
	account := svc.accounts["user1"]
	svc.mu.RUnlock()

	if account.RefreshToken != "new-refresh-token" {
		t.Errorf("refresh token should be updated, got %s", account.RefreshToken)
	}
	if account.Generation != 4 {
		t.Errorf("generation after refresh = %d, want 4", account.Generation)
	}
}

func TestResolveEntityParsesURI(t *testing.T) {
	tests := []struct {
		uri          string
		expectedType string
		expectedID   string
		shouldErr    bool
	}{
		{"spotify:track:abc123", "tracks", "abc123", false},
		{"spotify:album:xyz789", "albums", "xyz789", false},
		{"spotify:playlist:pl1", "playlists", "pl1", false},
		{"spotify:artist:ar1", "artists", "ar1", false},
		{"invalid-uri", "", "", true},
		{"spotify:invalid_type:id", "", "", true},
		{"spotify:track", "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.uri, func(t *testing.T) {
			entityType, entityID, err := parseSpotifyURI(tc.uri)
			if tc.shouldErr {
				if err == nil {
					t.Errorf("expected error for URI %s", tc.uri)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for URI %s: %v", tc.uri, err)
			}
			if entityType != tc.expectedType {
				t.Errorf("expected type %s, got %s", tc.expectedType, entityType)
			}
			if entityID != tc.expectedID {
				t.Errorf("expected id %s, got %s", tc.expectedID, entityID)
			}
		})
	}
}

func TestResolveEntityFetchesFromAPI(t *testing.T) {
	// Mock Spotify API
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check Authorization header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer fresh-token" {
			t.Errorf("expected Bearer fresh-token, got %s", auth)
		}

		switch r.URL.Path {
		case "/tracks/abc123":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name": "Test Track",
				"album": map[string]interface{}{
					"images": []map[string]interface{}{
						{"url": "http://img.example.com/track.jpg"},
					},
				},
			})
		case "/albums/xyz789":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name": "Test Album",
				"images": []map[string]interface{}{
					{"url": "http://img.example.com/album.jpg"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	svc := NewSpotifyService("cid", "csecret", "http://localhost/cb", t.TempDir())
	svc.apiBase = apiServer.URL

	// Add a non-expired account
	svc.mu.Lock()
	svc.accounts["user1"] = &Account{
		UserID:       "user1",
		AccessToken:  "fresh-token",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	svc.mu.Unlock()

	// Test track (images come from album)
	name, imageURL, err := svc.ResolveEntity("spotify:track:abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "Test Track" {
		t.Errorf("expected Test Track, got %s", name)
	}
	if imageURL != "http://img.example.com/track.jpg" {
		t.Errorf("expected track image URL, got %s", imageURL)
	}

	// Test album (images at top level)
	name, imageURL, err = svc.ResolveEntity("spotify:album:xyz789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "Test Album" {
		t.Errorf("expected Test Album, got %s", name)
	}
	if imageURL != "http://img.example.com/album.jpg" {
		t.Errorf("expected album image URL, got %s", imageURL)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	// Create and populate
	svc := NewSpotifyService("cid", "csecret", "http://localhost/cb", dir)
	svc.mu.Lock()
	svc.accounts["user1"] = &Account{
		UserID:       "user1",
		DisplayName:  "Test User",
		Email:        "test@example.com",
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    1234567890,
	}
	svc.accounts["user2"] = &Account{
		UserID:       "user2",
		DisplayName:  "User Two",
		Email:        "two@example.com",
		AccessToken:  "at2",
		RefreshToken: "rt2",
		ExpiresAt:    9876543210,
	}
	svc.mu.Unlock()

	// Save
	if err := svc.save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Verify file exists
	accountsFile := filepath.Join(dir, "spotify", "accounts.json")
	if _, err := os.Stat(accountsFile); os.IsNotExist(err) {
		t.Fatal("accounts.json was not created")
	}

	// Load into new service
	svc2 := NewSpotifyService("cid", "csecret", "http://localhost/cb", dir)
	if err := svc2.Load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	svc2.mu.RLock()
	defer svc2.mu.RUnlock()

	if len(svc2.accounts) != 2 {
		t.Fatalf("expected 2 accounts after load, got %d", len(svc2.accounts))
	}

	u1, ok := svc2.accounts["user1"]
	if !ok {
		t.Fatal("user1 not found after load")
	}
	if u1.DisplayName != "Test User" {
		t.Errorf("expected Test User, got %s", u1.DisplayName)
	}
	if u1.AccessToken != "at" {
		t.Errorf("expected at, got %s", u1.AccessToken)
	}
	if u1.ExpiresAt != 1234567890 {
		t.Errorf("expected ExpiresAt 1234567890, got %d", u1.ExpiresAt)
	}
}

func TestLoadAccountsWithoutGenerationDefaultsToZero(t *testing.T) {
	dir := t.TempDir()
	spotifyDir := filepath.Join(dir, "spotify")
	if err := os.MkdirAll(spotifyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"legacy":{"user_id":"legacy","access_token":"access","refresh_token":"refresh","expires_at":123}}`
	if err := os.WriteFile(filepath.Join(spotifyDir, "accounts.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	svc := NewSpotifyService("cid", "secret", "http://localhost/cb", dir)
	if err := svc.Load(); err != nil {
		t.Fatal(err)
	}
	if got := svc.accounts["legacy"].Generation; got != 0 {
		t.Fatalf("legacy account generation = %d, want 0", got)
	}
}

func TestSaveUsesPrivateAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	svc := NewSpotifyService("cid", "csecret", "http://localhost/cb", dir)
	svc.accounts["user1"] = &Account{UserID: "user1", AccessToken: "old-token"}
	if err := svc.save(); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	path := filepath.Join(dir, "spotify", "accounts.json")
	oldPath := filepath.Join(dir, "spotify", "old-accounts.json")
	if err := os.Link(path, oldPath); err != nil {
		t.Fatalf("link initial file: %v", err)
	}

	svc.accounts["user1"] = &Account{UserID: "user1", AccessToken: "new-token"}
	if err := svc.save(); err != nil {
		t.Fatalf("replacement save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("accounts.json mode = %04o, want 0600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("Spotify data directory mode = %04o, want 0700", got)
	}
	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info, oldInfo) {
		t.Fatal("accounts.json was modified in place instead of atomically replaced")
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "new-token") || strings.Contains(string(current), "old-token") {
		t.Fatalf("replacement contents = %s", current)
	}
	if !strings.Contains(string(old), "old-token") || strings.Contains(string(old), "new-token") {
		t.Fatalf("linked old inode was changed: %s", old)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".accounts-") {
			t.Fatalf("temporary persistence file left behind: %s", entry.Name())
		}
	}
}

func TestAccountPersistenceFailureBeforeRenamePreservesOldState(t *testing.T) {
	dir := t.TempDir()
	svc := NewSpotifyService("cid", "secret", "http://localhost/cb", dir)
	svc.accounts["user"] = &Account{UserID: "user", AccessToken: "old-token", Generation: 7}
	if err := svc.save(); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	beforeMemory := cloneAccounts(svc.accounts)
	beforeDisk := readAccountsFile(t, dir)
	wantErr := errors.New("rename failed")
	svc.renameFile = func(_, _ string) error { return wantErr }

	const secret = "bs-0123456789abcdef0123456789abcdef"
	if err := svc.AdoptBoseSecret("user", secret); !errors.Is(err, wantErr) {
		t.Fatalf("AdoptBoseSecret error = %v, want rename failure", err)
	}
	if !reflect.DeepEqual(svc.accounts, beforeMemory) {
		t.Fatalf("accounts changed before rename: got %#v want %#v", svc.accounts, beforeMemory)
	}
	if afterDisk := readAccountsFile(t, dir); !reflect.DeepEqual(afterDisk, beforeDisk) {
		t.Fatalf("accounts file changed before rename:\n%s\nwant:\n%s", afterDisk, beforeDisk)
	}
}

func TestAccountPersistenceFailureAfterRenameCommitsNewState(t *testing.T) {
	tests := []struct {
		name     string
		syncErr  error
		closeErr error
	}{
		{name: "directory sync", syncErr: errors.New("sync failed")},
		{name: "directory close", closeErr: errors.New("close failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			svc := NewSpotifyService("cid", "secret", "http://localhost/cb", dir)
			svc.accounts["user"] = &Account{UserID: "user", AccessToken: "old-token", Generation: 7}
			if err := svc.save(); err != nil {
				t.Fatalf("initial save: %v", err)
			}

			handle := &stubDirectorySyncCloser{syncErr: tt.syncErr, closeErr: tt.closeErr}
			svc.openDirectory = func(path string) (directorySyncCloser, error) {
				if want := filepath.Join(dir, "spotify"); path != want {
					t.Fatalf("opened directory %q, want %q", path, want)
				}
				return handle, nil
			}

			const secret = "bs-0123456789abcdef0123456789abcdef"
			if err := svc.AdoptBoseSecret("user", secret); err != nil {
				t.Fatalf("post-rename durability failure reported as rollback: %v", err)
			}
			if account := svc.accounts["user"]; account.BoseSecret != secret || account.Generation != 8 {
				t.Fatalf("committed in-memory account = %+v", account)
			}
			if !handle.syncCalled || !handle.closeCalled {
				t.Fatalf("directory calls: sync=%v close=%v, want both", handle.syncCalled, handle.closeCalled)
			}

			reloaded := NewSpotifyService("cid", "secret", "http://localhost/cb", dir)
			if err := reloaded.Load(); err != nil {
				t.Fatalf("load committed accounts: %v", err)
			}
			if account := reloaded.accounts["user"]; account == nil || account.BoseSecret != secret || account.Generation != 8 {
				t.Fatalf("committed on-disk account = %+v", account)
			}
		})
	}
}

func TestExchangeCodeAndStore(t *testing.T) {
	// Mock token endpoint
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}

		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "test-auth-code" {
				t.Errorf("expected code=test-auth-code, got %s", r.Form.Get("code"))
			}
			user, pass, ok := r.BasicAuth()
			if !ok || user != "cid" || pass != "csecret" {
				t.Errorf("bad Basic Auth: %s:%s ok=%v", user, pass, ok)
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "new-at",
				"refresh_token": "new-rt",
				"expires_in":    3600,
			})
		default:
			t.Errorf("unexpected grant_type: %s", r.Form.Get("grant_type"))
			http.Error(w, "bad request", 400)
		}
	}))
	defer tokenServer.Close()

	// Mock profile endpoint
	profileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer new-at" {
			t.Errorf("expected Bearer new-at, got %s", auth)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":           "spotify-user-123",
			"display_name": "Spotify User",
			"email":        "user@spotify.com",
		})
	}))
	defer profileServer.Close()

	dir := t.TempDir()
	svc := NewSpotifyService("cid", "csecret", "http://localhost/cb", dir)
	svc.tokenURL = tokenServer.URL
	svc.apiBase = profileServer.URL

	const preservedSecret = "bs-0123456789abcdef0123456789abcdef"
	svc.accounts["spotify-user-123"] = &Account{
		UserID:       "spotify-user-123",
		RefreshToken: "old-refresh-token",
		BoseSecret:   preservedSecret,
		Generation:   8,
	}

	linked, err := svc.ExchangeCodeAndStore("test-auth-code")
	if err != nil {
		t.Fatalf("ExchangeCodeAndStore failed: %v", err)
	}
	if linked != (LinkedAccount{UserID: "spotify-user-123", DisplayName: "Spotify User", BoseSecret: preservedSecret, Generation: 9}) {
		t.Fatalf("linked identity = %+v", linked)
	}

	// Verify account stored
	svc.mu.RLock()
	account, ok := svc.accounts["spotify-user-123"]
	svc.mu.RUnlock()

	if !ok {
		t.Fatal("account not found after exchange")
	}
	if account.DisplayName != "Spotify User" {
		t.Errorf("expected Spotify User, got %s", account.DisplayName)
	}
	if account.Email != "user@spotify.com" {
		t.Errorf("expected user@spotify.com, got %s", account.Email)
	}
	if account.AccessToken != "new-at" {
		t.Errorf("expected new-at, got %s", account.AccessToken)
	}
	if account.RefreshToken != "new-rt" {
		t.Errorf("expected new-rt, got %s", account.RefreshToken)
	}
	if account.BoseSecret != preservedSecret {
		t.Fatalf("BoseSecret changed during reauthorization: %q", account.BoseSecret)
	}
	if account.Generation != 9 {
		t.Fatalf("generation after authorization = %d, want 9", account.Generation)
	}

	// Verify saved to disk
	accountsFile := filepath.Join(dir, "spotify", "accounts.json")
	data, err := os.ReadFile(accountsFile)
	if err != nil {
		t.Fatalf("failed to read accounts file: %v", err)
	}
	if !strings.Contains(string(data), "spotify-user-123") {
		t.Error("accounts file should contain the user ID")
	}
}

func TestExchangeCodeAndStoreCSPRNGFailureLeavesStateUnchanged(t *testing.T) {
	svc, closeServers := newExchangeTestService(t, t.TempDir(), "new-user")
	defer closeServers()
	svc.accounts["existing"] = &Account{UserID: "existing", AccessToken: "keep-me"}
	before := cloneAccounts(svc.accounts)
	wantErr := errors.New("entropy unavailable")
	svc.random = failingReader{err: wantErr}

	if _, err := svc.ExchangeCodeAndStore("code"); !errors.Is(err, wantErr) {
		t.Fatalf("ExchangeCodeAndStore error = %v, want entropy failure", err)
	}
	if !reflect.DeepEqual(svc.accounts, before) {
		t.Fatalf("accounts changed after CSPRNG failure: got %#v want %#v", svc.accounts, before)
	}
}

func TestExchangeCodeAndStorePersistenceFailureLeavesStateUnchanged(t *testing.T) {
	base := t.TempDir()
	blockedDataDir := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(blockedDataDir, []byte("block mkdir"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, closeServers := newExchangeTestService(t, blockedDataDir, "new-user")
	defer closeServers()
	svc.accounts["existing"] = &Account{UserID: "existing", AccessToken: "keep-me"}
	svc.random = strings.NewReader("0123456789abcdef")
	before := cloneAccounts(svc.accounts)

	if _, err := svc.ExchangeCodeAndStore("code"); err == nil || !strings.Contains(err.Error(), "save accounts") {
		t.Fatalf("ExchangeCodeAndStore error = %v, want persistence failure", err)
	}
	if !reflect.DeepEqual(svc.accounts, before) {
		t.Fatalf("accounts changed after persistence failure: got %#v want %#v", svc.accounts, before)
	}
}

func newExchangeTestService(t *testing.T, dataDir, userID string) (*Service, func()) {
	t.Helper()
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	profileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": userID, "display_name": "New User"})
	}))
	svc := NewSpotifyService("cid", "secret", "http://localhost/cb", dataDir)
	svc.SetEndpoints(tokenServer.URL, profileServer.URL)
	return svc, func() {
		tokenServer.Close()
		profileServer.Close()
	}
}

func readAccountsFile(t *testing.T, dataDir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "spotify", "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestReconfigureDuringExchangeRejectsStalePersistence(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var profileRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token request: %v", err)
			}
			clientID, clientSecret, _ := r.BasicAuth()
			if clientID != "old-client" || clientSecret != "old-secret" {
				t.Errorf("token request credentials = %q/%q, want old config", clientID, clientSecret)
			}
			if got := r.Form.Get("redirect_uri"); got != "http://localhost/old" {
				t.Errorf("token request redirect_uri = %q, want old config", got)
			}
			close(requestStarted)
			<-releaseRequest
			_, _ = io.WriteString(w, `{"access_token":"stale-access","refresh_token":"stale-refresh","expires_in":3600}`)
		case "/me":
			profileRequests.Add(1)
			_, _ = io.WriteString(w, `{"id":"new-user","display_name":"Stale User"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	svc := NewSpotifyService("old-client", "old-secret", "http://localhost/old", dir)
	svc.SetEndpoints(server.URL+"/token", server.URL)
	svc.accounts["existing"] = &Account{UserID: "existing", AccessToken: "keep", RefreshToken: "keep-refresh", Generation: 2}
	if err := svc.save(); err != nil {
		t.Fatal(err)
	}
	beforeMemory := cloneAccounts(svc.accounts)
	beforeDisk := readAccountsFile(t, dir)

	errCh := make(chan error, 1)
	go func() {
		_, err := svc.ExchangeCodeAndStore("code")
		errCh <- err
	}()
	<-requestStarted
	svc.Reconfigure("new-client", "new-secret", "http://localhost/new")
	close(releaseRequest)

	if err := <-errCh; !errors.Is(err, errReconfigured) {
		t.Fatalf("ExchangeCodeAndStore error = %v, want reconfigured error", err)
	}
	if got := profileRequests.Load(); got != 1 {
		t.Fatalf("profile requests = %d, want 1 using captured config", got)
	}
	if !reflect.DeepEqual(svc.accounts, beforeMemory) {
		t.Fatalf("accounts changed after stale exchange: got %#v want %#v", svc.accounts, beforeMemory)
	}
	if afterDisk := readAccountsFile(t, dir); !reflect.DeepEqual(afterDisk, beforeDisk) {
		t.Fatalf("accounts file changed after stale exchange:\n%s\nwant:\n%s", afterDisk, beforeDisk)
	}
}

func TestReconfigureDuringRefreshRejectsStalePersistence(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID, clientSecret, _ := r.BasicAuth()
		if clientID != "old-client" || clientSecret != "old-secret" {
			t.Errorf("refresh credentials = %q/%q, want old config", clientID, clientSecret)
		}
		close(requestStarted)
		<-releaseRequest
		_, _ = io.WriteString(w, `{"access_token":"stale-access","refresh_token":"stale-refresh","expires_in":3600}`)
	}))
	defer server.Close()

	dir := t.TempDir()
	svc := NewSpotifyService("old-client", "old-secret", "http://localhost/old", dir)
	svc.SetEndpoints(server.URL, "")
	svc.accounts["user"] = &Account{
		UserID:       "user",
		AccessToken:  "current-access",
		RefreshToken: "current-refresh",
		ExpiresAt:    1,
		Generation:   6,
	}
	if err := svc.save(); err != nil {
		t.Fatal(err)
	}
	beforeMemory := cloneAccounts(svc.accounts)
	beforeDisk := readAccountsFile(t, dir)

	errCh := make(chan error, 1)
	go func() { errCh <- svc.RefreshAccessToken(&Account{UserID: "user"}) }()
	<-requestStarted
	svc.Reconfigure("new-client", "new-secret", "http://localhost/new")
	close(releaseRequest)

	if err := <-errCh; !errors.Is(err, errReconfigured) {
		t.Fatalf("RefreshAccessToken error = %v, want reconfigured error", err)
	}
	if !reflect.DeepEqual(svc.accounts, beforeMemory) {
		t.Fatalf("accounts changed after stale refresh: got %#v want %#v", svc.accounts, beforeMemory)
	}
	if afterDisk := readAccountsFile(t, dir); !reflect.DeepEqual(afterDisk, beforeDisk) {
		t.Fatalf("accounts file changed after stale refresh:\n%s\nwant:\n%s", afterDisk, beforeDisk)
	}
}

func TestSameRefreshTokenReauthorizationFencesInFlightRefresh(t *testing.T) {
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token request: %v", err)
			}
			switch r.Form.Get("grant_type") {
			case "refresh_token":
				close(refreshStarted)
				<-releaseRefresh
				_, _ = io.WriteString(w, `{"access_token":"stale-refresh-access","refresh_token":"same-refresh","expires_in":3600}`)
			case "authorization_code":
				_, _ = io.WriteString(w, `{"access_token":"reauthorized-access","refresh_token":"same-refresh","expires_in":3600}`)
			default:
				http.Error(w, "unexpected grant", http.StatusBadRequest)
			}
		case "/me":
			_, _ = io.WriteString(w, `{"id":"user","display_name":"Reauthorized User"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	svc := NewSpotifyService("client", "secret", "http://localhost/callback", dir)
	svc.SetEndpoints(server.URL+"/token", server.URL)
	svc.accounts["user"] = &Account{
		UserID:       "user",
		DisplayName:  "Old User",
		AccessToken:  "old-access",
		RefreshToken: "same-refresh",
		ExpiresAt:    1,
		BoseSecret:   "bs-0123456789abcdef0123456789abcdef",
		Generation:   11,
	}
	if err := svc.save(); err != nil {
		t.Fatal(err)
	}

	refreshErr := make(chan error, 1)
	go func() { refreshErr <- svc.RefreshAccessToken(&Account{UserID: "user"}) }()
	<-refreshStarted

	if _, err := svc.ExchangeCodeAndStore("reauthorize"); err != nil {
		t.Fatalf("reauthorization failed: %v", err)
	}
	afterAuthorization := cloneAccounts(svc.accounts)
	afterAuthorizationDisk := readAccountsFile(t, dir)
	if got := afterAuthorization["user"].Generation; got != 12 {
		t.Fatalf("generation after reauthorization = %d, want 12", got)
	}
	close(releaseRefresh)

	if err := <-refreshErr; err != nil {
		t.Fatalf("stale refresh returned error: %v", err)
	}
	if !reflect.DeepEqual(svc.accounts, afterAuthorization) {
		t.Fatalf("stale refresh overwrote memory: got %#v want %#v", svc.accounts, afterAuthorization)
	}
	if afterDisk := readAccountsFile(t, dir); !reflect.DeepEqual(afterDisk, afterAuthorizationDisk) {
		t.Fatalf("stale refresh overwrote accounts file:\n%s\nwant:\n%s", afterDisk, afterAuthorizationDisk)
	}
}

func TestGetFreshTokenRejectsAmbiguousAccounts(t *testing.T) {
	svc := NewSpotifyService("cid", "secret", "http://localhost/cb", t.TempDir())
	expires := time.Now().Add(time.Hour).Unix()
	svc.accounts["user-a"] = &Account{UserID: "user-a", AccessToken: "token-a", RefreshToken: "refresh-a", ExpiresAt: expires}
	svc.accounts["user-b"] = &Account{UserID: "user-b", AccessToken: "token-b", RefreshToken: "refresh-b", ExpiresAt: expires}

	if _, _, err := svc.GetFreshToken(); !errors.Is(err, ErrAmbiguousAccount) {
		t.Fatalf("GetFreshToken error = %v, want ErrAmbiguousAccount", err)
	}
	token, userID, err := svc.GetFreshTokenForUser("user-b")
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-b" || userID != "user-b" {
		t.Fatalf("exact selection = (%q, %q), want token-b/user-b", token, userID)
	}
}

func TestConcurrentRefreshIsSerialized(t *testing.T) {
	var requests atomic.Int32
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var startedOnce sync.Once

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		startedOnce.Do(func() { close(requestStarted) })
		<-releaseRequest
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"refreshed","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	svc := NewSpotifyService("cid", "secret", "http://localhost/cb", t.TempDir())
	svc.tokenURL = tokenServer.URL
	svc.accounts["user"] = &Account{UserID: "user", RefreshToken: "refresh", ExpiresAt: 1}

	const callers = 12
	start := make(chan struct{})
	errs := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			ready.Done()
			<-start
			errs <- svc.RefreshAccessToken(&Account{UserID: "user"})
		}()
	}
	ready.Wait()
	close(start)
	<-requestStarted
	time.Sleep(20 * time.Millisecond)
	close(releaseRequest)

	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("refresh caller failed: %v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("token endpoint requests = %d, want 1", got)
	}
	if account := svc.accounts["user"]; account.AccessToken != "refreshed" || account.Generation != 1 {
		t.Fatalf("account after concurrent refresh = %+v", account)
	}

	reloaded := NewSpotifyService("cid", "secret", "http://localhost/cb", svc.dataDir)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("load account after concurrent refresh: %v", err)
	}
	if account := reloaded.accounts["user"]; account == nil || account.AccessToken != "refreshed" || account.Generation != 1 {
		t.Fatalf("persisted account after concurrent refresh = %+v", account)
	}
}

func TestRefreshHonorsHTTPTimeout(t *testing.T) {
	svc := NewSpotifyService("cid", "secret", "http://localhost/cb", t.TempDir())
	svc.accounts["user"] = &Account{UserID: "user", RefreshToken: "refresh", ExpiresAt: 1}
	svc.httpClient = &http.Client{
		Timeout: 20 * time.Millisecond,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}

	started := time.Now()
	err := svc.RefreshAccessToken(&Account{UserID: "user"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RefreshAccessToken error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("HTTP timeout took too long: %v", elapsed)
	}
}

func TestInvalidGrantClearsTokensPersistsReauthAndPreservesBoseSecret(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	defer tokenServer.Close()

	dir := t.TempDir()
	const secret = "bs-0123456789abcdef0123456789abcdef"
	svc := NewSpotifyService("cid", "secret", "http://localhost/cb", dir)
	svc.tokenURL = tokenServer.URL
	svc.accounts["user"] = &Account{
		UserID:       "user",
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    1,
		BoseSecret:   secret,
		Generation:   5,
	}
	if err := svc.save(); err != nil {
		t.Fatal(err)
	}

	err := svc.RefreshAccessToken(&Account{UserID: "user"})
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("RefreshAccessToken error = %v, want ErrReauthRequired", err)
	}
	account := svc.accounts["user"]
	if account.AccessToken != "" || account.RefreshToken != "" || account.ExpiresAt != 0 || !account.ReauthRequired {
		t.Fatalf("reauth state = %+v", account)
	}
	if account.BoseSecret != secret {
		t.Fatalf("BoseSecret = %q, want preserved secret", account.BoseSecret)
	}
	if account.Generation != 6 {
		t.Fatalf("generation after invalid_grant = %d, want 6", account.Generation)
	}

	reloaded := NewSpotifyService("cid", "secret", "http://localhost/cb", dir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	persisted := reloaded.accounts["user"]
	if persisted == nil || !persisted.ReauthRequired || persisted.AccessToken != "" || persisted.RefreshToken != "" || persisted.BoseSecret != secret || persisted.Generation != 6 {
		t.Fatalf("persisted reauth state = %+v", persisted)
	}
}

func TestAdoptBoseSecretIncrementsAndPersistsAccountGeneration(t *testing.T) {
	dir := t.TempDir()
	svc := NewSpotifyService("cid", "secret", "http://localhost/cb", dir)
	svc.accounts["user"] = &Account{UserID: "user", Generation: 9}
	if err := svc.save(); err != nil {
		t.Fatal(err)
	}

	const secret = "bs-0123456789abcdef0123456789abcdef"
	if err := svc.AdoptBoseSecret("user", secret); err != nil {
		t.Fatal(err)
	}
	if account := svc.accounts["user"]; account.BoseSecret != secret || account.Generation != 10 {
		t.Fatalf("adopted account = %+v", account)
	}

	reloaded := NewSpotifyService("cid", "secret", "http://localhost/cb", dir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if account := reloaded.accounts["user"]; account == nil || account.BoseSecret != secret || account.Generation != 10 {
		t.Fatalf("persisted adopted account = %+v", account)
	}
}

func TestGetFreshTokenNoAccounts(t *testing.T) {
	svc := NewSpotifyService("cid", "csecret", "http://localhost/cb", t.TempDir())

	_, _, err := svc.GetFreshToken()
	if err == nil {
		t.Error("expected error when no accounts exist")
	}
}

func TestGetFreshTokenNotExpired(t *testing.T) {
	svc := NewSpotifyService("cid", "csecret", "http://localhost/cb", t.TempDir())

	svc.mu.Lock()
	svc.accounts["user1"] = &Account{
		UserID:       "user1",
		AccessToken:  "valid-token",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	svc.mu.Unlock()

	token, username, err := svc.GetFreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "valid-token" {
		t.Errorf("expected valid-token, got %s", token)
	}
	if username != "user1" {
		t.Errorf("expected user1, got %s", username)
	}
}
