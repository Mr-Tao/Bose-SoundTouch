// Package spotify provides Spotify OAuth integration and token management
// for the SoundTouch service, ported from soundcork's Python implementation.
package spotify

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// SpotifyAuthorizeURL is the Spotify OAuth authorization endpoint.
	SpotifyAuthorizeURL = "https://accounts.spotify.com/authorize"
	// SpotifyTokenURL is the Spotify OAuth token endpoint.
	SpotifyTokenURL = "https://accounts.spotify.com/api/token"
	// SpotifyAPIBase is the base URL for the Spotify Web API.
	SpotifyAPIBase = "https://api.spotify.com/v1"
	// SpotifyScopes are the OAuth scopes required for speaker playback and user info.
	SpotifyScopes = "streaming user-read-private user-read-email user-read-playback-state user-modify-playback-state"

	spotifyHTTPTimeout = 15 * time.Second
)

var (
	ErrNoAccounts       = errors.New("no Spotify accounts linked")
	ErrAmbiguousAccount = errors.New("multiple Spotify accounts linked; an explicit account is required")
	ErrAccountNotFound  = errors.New("Spotify account not found")
	ErrReauthRequired   = errors.New("Spotify account requires reauthorization")
	errReconfigured     = errors.New("Spotify service reconfigured during operation")
)

// Account represents a stored Spotify account with tokens.
type Account struct {
	UserID         string `json:"user_id"`
	DisplayName    string `json:"display_name"`
	Email          string `json:"email"`
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token"`
	ExpiresAt      int64  `json:"expires_at"`
	BoseSecret     string `json:"bose_secret,omitempty"`
	AuthorizedAt   int64  `json:"authorized_at,omitempty"`
	ReauthRequired bool   `json:"reauth_required,omitempty"`
	Generation     uint64 `json:"generation,omitempty"`
}

// AccountSummary is the non-secret representation exposed by management APIs.
type AccountSummary struct {
	UserID         string `json:"user_id"`
	DisplayName    string `json:"display_name"`
	Email          string `json:"email"`
	ExpiresAt      int64  `json:"expires_at"`
	AuthorizedAt   int64  `json:"authorized_at,omitempty"`
	ReauthRequired bool   `json:"reauth_required,omitempty"`
}

// LinkedAccount contains the minimum internal data needed to bridge a Spotify
// identity into Marge. It must never be serialized directly to a client.
type LinkedAccount struct {
	UserID      string
	DisplayName string
	BoseSecret  string
	Generation  uint64
}

// AuthorizationExchange contains provider data fetched during an OAuth code
// exchange but not yet committed to the linked-account store.
type AuthorizationExchange struct {
	userID           string
	displayName      string
	email            string
	accessToken      string
	refreshToken     string
	expiresAt        int64
	authorizedAt     int64
	configGeneration uint64
}

// Service manages Spotify OAuth flow and token lifecycle.
type Service struct {
	clientID         string
	clientSecret     string
	redirectURI      string
	dataDir          string
	mu               sync.RWMutex
	accounts         map[string]*Account
	configGeneration uint64
	refreshes        singleflight.Group
	httpClient       *http.Client
	random           io.Reader
	renameFile       func(string, string) error
	openDirectory    func(string) (directorySyncCloser, error)

	// Overridable URLs for testing
	tokenURL string
	apiBase  string
}

type directorySyncCloser interface {
	Sync() error
	Close() error
}

type configSnapshot struct {
	clientID     string
	clientSecret string
	redirectURI  string
	tokenURL     string
	apiBase      string
	httpClient   *http.Client
	generation   uint64
}

// NewSpotifyService creates a new Service and loads any persisted accounts.
func NewSpotifyService(clientID, clientSecret, redirectURI, dataDir string) *Service {
	return &Service{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  redirectURI,
		dataDir:      dataDir,
		accounts:     make(map[string]*Account),
		httpClient:   &http.Client{Timeout: spotifyHTTPTimeout},
		random:       rand.Reader,
		renameFile:   os.Rename,
		openDirectory: func(path string) (directorySyncCloser, error) {
			return os.Open(path)
		},
		tokenURL: SpotifyTokenURL,
		apiBase:  SpotifyAPIBase,
	}
}

// Load loads persisted accounts from disk.
func (s *Service) Load() error {
	if err := s.load(); err != nil {
		return err
	}

	return nil
}

// Reconfigure updates the OAuth client settings without replacing the service
// or its loaded accounts. In-flight operations cannot persist after this call.
func (s *Service) Reconfigure(clientID, clientSecret, redirectURI string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.clientID = clientID
	s.clientSecret = clientSecret
	s.redirectURI = redirectURI
	s.configGeneration++
}

// SetEndpoints allows overriding default Spotify API endpoints (for testing).
func (s *Service) SetEndpoints(tokenURL, apiBase string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokenURL = tokenURL
	s.apiBase = apiBase
	s.configGeneration++
}

func (s *Service) snapshotConfig() configSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return configSnapshot{
		clientID:     s.clientID,
		clientSecret: s.clientSecret,
		redirectURI:  s.redirectURI,
		tokenURL:     s.tokenURL,
		apiBase:      s.apiBase,
		httpClient:   s.httpClient,
		generation:   s.configGeneration,
	}
}

// BuildAuthorizeURL constructs the Spotify OAuth authorization URL.
func (s *Service) BuildAuthorizeURL(state string) string {
	config := s.snapshotConfig()
	params := url.Values{
		"client_id":     {config.clientID},
		"response_type": {"code"},
		"redirect_uri":  {config.redirectURI},
		"scope":         {SpotifyScopes},
	}
	if state != "" {
		params.Set("state", state)
	}

	return SpotifyAuthorizeURL + "?" + params.Encode()
}

// ExchangeCodeAndStore exchanges an authorization code for tokens, fetches the
// user profile, and stores the exact linked identity. Reauthorizing an existing
// identity preserves its Bose surrogate secret.
func (s *Service) ExchangeCodeAndStore(code string) (LinkedAccount, error) {
	exchange, err := s.ExchangeCode(code)
	if err != nil {
		return LinkedAccount{}, err
	}

	return s.StoreAuthorizationExchange(exchange)
}

// ExchangeCode fetches provider tokens and profile data without mutating the
// linked-account store. Callers that need an ownership fence can validate it
// before StoreAuthorizationExchange persists the result.
func (s *Service) ExchangeCode(code string) (AuthorizationExchange, error) {
	config := s.snapshotConfig()

	// Exchange code for tokens
	tokenResp, err := s.exchangeCode(config, code)
	if err != nil {
		return AuthorizationExchange{}, fmt.Errorf("token exchange: %w", err)
	}

	accessToken, _ := tokenResp["access_token"].(string)
	refreshToken, _ := tokenResp["refresh_token"].(string)
	if accessToken == "" {
		return AuthorizationExchange{}, fmt.Errorf("token exchange returned no access token")
	}

	expiresIn, _ := tokenResp["expires_in"].(float64)
	if expiresIn == 0 {
		expiresIn = 3600
	}

	// Fetch user profile
	profile, err := s.getUserProfile(config, accessToken)
	if err != nil {
		return AuthorizationExchange{}, fmt.Errorf("fetch profile: %w", err)
	}

	userID, _ := profile["id"].(string)
	displayName, _ := profile["display_name"].(string)
	email, _ := profile["email"].(string)
	if userID == "" {
		return AuthorizationExchange{}, fmt.Errorf("Spotify profile returned no user ID")
	}

	now := time.Now().Unix()
	return AuthorizationExchange{
		userID:           userID,
		displayName:      displayName,
		email:            email,
		accessToken:      accessToken,
		refreshToken:     refreshToken,
		expiresAt:        now + int64(expiresIn),
		authorizedAt:     now,
		configGeneration: config.generation,
	}, nil
}

// StoreAuthorizationExchange conditionally commits a completed provider
// exchange. It rejects data fetched under an obsolete service configuration.
func (s *Service) StoreAuthorizationExchange(exchange AuthorizationExchange) (LinkedAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != exchange.configGeneration {
		return LinkedAccount{}, errReconfigured
	}

	var boseSecret string
	var accountGeneration uint64
	refreshToken := exchange.refreshToken
	if existing := s.accounts[exchange.userID]; existing != nil {
		boseSecret = existing.BoseSecret
		accountGeneration = existing.Generation
		if refreshToken == "" {
			refreshToken = existing.RefreshToken
		}
	}

	if boseSecret == "" {
		generatedSecret, err := s.generateBoseSecret()
		if err != nil {
			return LinkedAccount{}, fmt.Errorf("generate Bose surrogate secret: %w", err)
		}
		boseSecret = generatedSecret
	}

	if refreshToken == "" {
		return LinkedAccount{}, fmt.Errorf("token exchange returned no refresh token")
	}

	account := &Account{
		UserID:       exchange.userID,
		DisplayName:  exchange.displayName,
		Email:        exchange.email,
		AccessToken:  exchange.accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    exchange.expiresAt,
		BoseSecret:   boseSecret,
		AuthorizedAt: exchange.authorizedAt,
		Generation:   accountGeneration + 1,
	}

	next := cloneAccounts(s.accounts)
	next[exchange.userID] = account
	if err := s.persistAccounts(next); err != nil {
		return LinkedAccount{}, fmt.Errorf("save accounts: %w", err)
	}
	s.accounts = next

	log.Printf("[Spotify] Account linked: %s (%s)", exchange.displayName, exchange.userID)

	return LinkedAccount{UserID: exchange.userID, DisplayName: exchange.displayName, BoseSecret: boseSecret, Generation: account.Generation}, nil
}

func (s *Service) exchangeCode(config configSnapshot, code string) (map[string]interface{}, error) {
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {config.redirectURI},
	}

	req, err := http.NewRequest(http.MethodPost, config.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(config.clientID, config.clientSecret)

	resp, err := config.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return result, nil
}

func (s *Service) getUserProfile(config configSnapshot, accessToken string) (map[string]interface{}, error) {
	req, err := http.NewRequest(http.MethodGet, config.apiBase+"/me", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := config.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("profile request: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("profile fetch failed (%d): %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse profile: %w", err)
	}

	return result, nil
}

// RefreshAccessToken refreshes the access token for the given account. The
// account pointer is used only to select the identity; callers never receive a
// mutable pointer into Service state.
func (s *Service) RefreshAccessToken(account *Account) error {
	if account == nil || account.UserID == "" {
		return ErrAccountNotFound
	}

	_, err, _ := s.refreshes.Do(account.UserID, func() (interface{}, error) {
		return nil, s.refreshAccessTokenForUser(account.UserID)
	})

	return err
}

func (s *Service) refreshAccessTokenForUser(userID string) error {
	s.mu.RLock()
	account := cloneAccount(s.accounts[userID])
	config := configSnapshot{
		clientID:     s.clientID,
		clientSecret: s.clientSecret,
		redirectURI:  s.redirectURI,
		tokenURL:     s.tokenURL,
		apiBase:      s.apiBase,
		httpClient:   s.httpClient,
		generation:   s.configGeneration,
	}
	s.mu.RUnlock()

	if account == nil {
		return ErrAccountNotFound
	}
	if account.ReauthRequired || account.RefreshToken == "" {
		return ErrReauthRequired
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {account.RefreshToken},
	}

	req, err := http.NewRequest(http.MethodPost, config.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(config.clientID, config.clientSecret)

	resp, err := config.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("refresh request: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var spotifyError struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &spotifyError)

		if spotifyError.Error == "invalid_grant" {
			transitioned, err := s.markReauthRequired(userID, config.generation, account.Generation)
			if err != nil {
				return fmt.Errorf("mark account for reauthorization: %w", err)
			}
			if transitioned {
				return ErrReauthRequired
			}

			return nil
		}

		if spotifyError.Error == "" {
			spotifyError.Error = "unknown_error"
		}

		return fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, spotifyError.Error)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	accessToken, _ := result["access_token"].(string)
	if accessToken == "" {
		return fmt.Errorf("token refresh returned no access token")
	}

	expiresIn, _ := result["expires_in"].(float64)
	if expiresIn == 0 {
		expiresIn = 3600
	}

	account.AccessToken = accessToken
	account.ExpiresAt = time.Now().Unix() + int64(expiresIn)
	if newRefresh, ok := result["refresh_token"].(string); ok && newRefresh != "" {
		account.RefreshToken = newRefresh
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != config.generation {
		return errReconfigured
	}

	current := s.accounts[userID]
	if current == nil {
		return ErrAccountNotFound
	}
	if current.Generation != account.Generation {
		// A newer account transition won the race. The refresh token may be
		// unchanged across reauthorization, so generation is the stale fence.
		return nil
	}

	account.Generation++
	next := cloneAccounts(s.accounts)
	next[userID] = account
	if err := s.persistAccounts(next); err != nil {
		return fmt.Errorf("save accounts: %w", err)
	}
	s.accounts = next

	return nil
}

func (s *Service) markReauthRequired(userID string, configGeneration, accountGeneration uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configGeneration != configGeneration {
		return false, errReconfigured
	}

	current := s.accounts[userID]
	if current == nil {
		return false, ErrAccountNotFound
	}
	if current.Generation != accountGeneration {
		return false, nil
	}

	next := cloneAccounts(s.accounts)
	nextAccount := next[userID]
	nextAccount.AccessToken = ""
	nextAccount.RefreshToken = ""
	nextAccount.ExpiresAt = 0
	nextAccount.ReauthRequired = true
	nextAccount.Generation++
	if err := s.persistAccounts(next); err != nil {
		return false, err
	}
	s.accounts = next

	return true, nil
}

// GetFreshToken returns a token only when exactly one identity is linked.
// Multi-account callers must use GetFreshTokenForUser.
func (s *Service) GetFreshToken() (accessToken, username string, err error) {
	s.mu.RLock()
	if len(s.accounts) == 0 {
		s.mu.RUnlock()
		return "", "", ErrNoAccounts
	}
	if len(s.accounts) != 1 {
		s.mu.RUnlock()
		return "", "", ErrAmbiguousAccount
	}

	var userID string
	for id := range s.accounts {
		userID = id
		break
	}
	s.mu.RUnlock()

	return s.GetFreshTokenForUser(userID)
}

// GetFreshTokenForUser returns a valid token for one explicit Spotify identity.
func (s *Service) GetFreshTokenForUser(userID string) (accessToken, username string, err error) {
	s.mu.RLock()
	account := cloneAccount(s.accounts[userID])
	s.mu.RUnlock()

	if account == nil {
		return "", "", ErrAccountNotFound
	}
	if account.ReauthRequired || account.RefreshToken == "" {
		return "", "", ErrReauthRequired
	}

	if account.ExpiresAt < time.Now().Unix()+60 {
		if err := s.RefreshAccessToken(account); err != nil {
			return "", "", fmt.Errorf("refresh token: %w", err)
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	account = s.accounts[userID]
	if account == nil {
		return "", "", ErrAccountNotFound
	}
	if account.ReauthRequired || account.AccessToken == "" {
		return "", "", ErrReauthRequired
	}

	return account.AccessToken, account.UserID, nil
}

// GetAccounts returns non-secret account summaries suitable for API responses.
func (s *Service) GetAccounts() []AccountSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]AccountSummary, 0, len(s.accounts))
	for _, a := range s.accounts {
		result = append(result, AccountSummary{
			UserID:         a.UserID,
			DisplayName:    a.DisplayName,
			Email:          a.Email,
			ExpiresAt:      a.ExpiresAt,
			AuthorizedAt:   a.AuthorizedAt,
			ReauthRequired: a.ReauthRequired,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UserID < result[j].UserID })

	return result
}

// GetLinkedAccount returns bridge data for one explicit Spotify identity.
func (s *Service) GetLinkedAccount(userID string) (LinkedAccount, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	a := s.accounts[userID]
	if a == nil || a.BoseSecret == "" {
		return LinkedAccount{}, false
	}

	return LinkedAccount{UserID: a.UserID, DisplayName: a.DisplayName, BoseSecret: a.BoseSecret, Generation: a.Generation}, true
}

// AdoptBoseSecret repairs an old account record only when an exact configured
// source already carries a surrogate generated by this service. Raw OAuth
// access tokens are deliberately rejected as long-lived speaker credentials.
func (s *Service) AdoptBoseSecret(userID, secret string) error {
	if !isBoseSurrogateSecret(secret) {
		return fmt.Errorf("invalid Bose surrogate format")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.accounts[userID]
	if current == nil {
		return ErrAccountNotFound
	}
	if current.BoseSecret != "" {
		if current.BoseSecret != secret {
			return fmt.Errorf("existing Bose surrogate differs")
		}
		return nil
	}

	next := cloneAccounts(s.accounts)
	next[userID].BoseSecret = secret
	next[userID].Generation++
	if err := s.persistAccounts(next); err != nil {
		return err
	}
	s.accounts = next

	return nil
}

func isBoseSurrogateSecret(secret string) bool {
	if len(secret) != len("bs-")+32 || !strings.HasPrefix(secret, "bs-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(secret, "bs-"))

	return err == nil
}

// GetUserIDBySecret resolves a Bose surrogate without exposing it to callers.
func (s *Service) GetUserIDBySecret(secret string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if secret == "" {
		return "", false
	}
	for _, a := range s.accounts {
		if a.BoseSecret == secret {
			return a.UserID, true
		}
	}

	return "", false
}

func (s *Service) generateBoseSecret() (string, error) {
	prefix := "bs-"

	b := make([]byte, 16)
	if _, err := io.ReadFull(s.random, b); err != nil {
		return "", err
	}

	return prefix + hex.EncodeToString(b), nil
}

// ResolveEntity resolves a Spotify URI to a name and image URL.
func (s *Service) ResolveEntity(uri string) (name, imageURL string, err error) {
	_, userID, err := s.GetFreshToken()
	if err != nil {
		return "", "", fmt.Errorf("get token: %w", err)
	}

	return s.ResolveEntityForUser(userID, uri)
}

// ResolveEntityForUser resolves a Spotify URI using one explicit account.
func (s *Service) ResolveEntityForUser(userID, uri string) (name, imageURL string, err error) {
	entityType, entityID, err := parseSpotifyURI(uri)
	if err != nil {
		return "", "", err
	}

	accessToken, _, err := s.GetFreshTokenForUser(userID)
	if err != nil {
		return "", "", fmt.Errorf("get token: %w", err)
	}
	config := s.snapshotConfig()

	apiURL := fmt.Sprintf("%s/%s/%s", config.apiBase, entityType, entityID)

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := config.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("API request: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", "", fmt.Errorf("spotify entity not found")
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("spotify API error (%d): %s", resp.StatusCode, string(body))
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", "", fmt.Errorf("parse response: %w", err)
	}

	name, _ = data["name"].(string)
	if name == "" {
		name = "Unknown"
	}

	// Extract image URL — location varies by entity type
	imageURL = extractImageURL(data, entityType)

	return name, imageURL, nil
}

// extractImageURL extracts the first image URL from a Spotify API response.
// For tracks, images are stored on the album object.
func extractImageURL(data map[string]interface{}, entityType string) string {
	images, _ := data["images"].([]interface{})
	if len(images) == 0 && entityType == "tracks" {
		// Tracks store images on the album
		album, _ := data["album"].(map[string]interface{})
		if album != nil {
			images, _ = album["images"].([]interface{})
		}
	}

	if len(images) > 0 {
		if img, ok := images[0].(map[string]interface{}); ok {
			url, _ := img["url"].(string)
			return url
		}
	}

	return ""
}

// parseSpotifyURI parses a Spotify URI like "spotify:track:abc" into
// the pluralized API type ("tracks") and ID ("abc").
func parseSpotifyURI(uri string) (entityType, entityID string, err error) {
	parts := strings.Split(uri, ":")
	if len(parts) != 3 || parts[0] != "spotify" {
		return "", "", fmt.Errorf("invalid Spotify URI format: %s", uri)
	}

	typ := parts[1]
	id := parts[2]

	validTypes := map[string]string{
		"track":    "tracks",
		"album":    "albums",
		"playlist": "playlists",
		"artist":   "artists",
	}

	plural, ok := validTypes[typ]
	if !ok {
		return "", "", fmt.Errorf("unsupported Spotify entity type: %s", typ)
	}

	return plural, id, nil
}

// save persists accounts to disk as JSON.
func (s *Service) save() error {
	s.mu.RLock()
	data := cloneAccounts(s.accounts)
	s.mu.RUnlock()

	return s.persistAccounts(data)
}

func cloneAccount(account *Account) *Account {
	if account == nil {
		return nil
	}

	copy := *account

	return &copy
}

func cloneAccounts(accounts map[string]*Account) map[string]*Account {
	result := make(map[string]*Account, len(accounts))
	for userID, account := range accounts {
		result[userID] = cloneAccount(account)
	}

	return result
}

func (s *Service) persistAccounts(accounts map[string]*Account) error {
	jsonData, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal accounts: %w", err)
	}

	dir := filepath.Join(s.dataDir, "spotify")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("secure directory: %w", err)
	}

	path := filepath.Join(dir, "accounts.json")
	tmp, err := os.CreateTemp(dir, ".accounts-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temporary file: %w", err)
	}
	if _, err := tmp.Write(jsonData); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := s.renameFile(tmpPath, path); err != nil {
		return fmt.Errorf("replace accounts file: %w", err)
	}
	removeTemp = false

	dirHandle, err := s.openDirectory(dir)
	if err != nil {
		log.Printf("[Spotify] Warning: accounts file replaced but opening its directory for durability sync failed: %v", err)

		return nil
	}
	if err := dirHandle.Sync(); err != nil {
		_ = dirHandle.Close()
		log.Printf("[Spotify] Warning: accounts file replaced but directory durability sync failed: %v", err)

		return nil
	}
	if err := dirHandle.Close(); err != nil {
		log.Printf("[Spotify] Warning: accounts file replaced but closing its directory after durability sync failed: %v", err)
	}

	return nil
}

// load reads persisted accounts from disk.
func (s *Service) load() error {
	path := filepath.Join(s.dataDir, "spotify", "accounts.json")

	jsonData, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No accounts file yet, not an error
		}

		return fmt.Errorf("read file: %w", err)
	}

	var accounts map[string]*Account
	if err := json.Unmarshal(jsonData, &accounts); err != nil {
		return fmt.Errorf("unmarshal accounts: %w", err)
	}
	if accounts == nil {
		accounts = make(map[string]*Account)
	}
	for userID, account := range accounts {
		if account == nil {
			return fmt.Errorf("account %q is null", userID)
		}
		if account.UserID == "" {
			account.UserID = userID
		}
		if account.UserID != userID {
			return fmt.Errorf("account key %q does not match user_id %q", userID, account.UserID)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("secure accounts file: %w", err)
	}

	s.mu.Lock()
	s.accounts = cloneAccounts(accounts)
	s.mu.Unlock()

	log.Printf("[Spotify] Loaded %d account(s)", len(accounts))

	return nil
}
