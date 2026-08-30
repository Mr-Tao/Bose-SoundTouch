package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"strconv"

	"github.com/gesellix/bose-soundtouch/pkg/service/amazon"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/spotify"
	"github.com/go-chi/chi/v5"
)

// HandleBoseToken handles the Bose-specific token refresh request from the speaker.
// POST /oauth/device/{deviceID}/music/musicprovider/{sourceID}/token/cs1
// POST /oauth/device/{deviceID}/music/musicprovider/{sourceID}/token/cs3
func (s *Server) HandleBoseToken(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")

	for _, provider := range constants.StaticProviders {
		if strconv.Itoa(provider.ID) != sourceID {
			continue
		}

		switch provider.Name {
		case constants.ProviderSpotify:
			s.HandleBoseSpotifyToken(w, r)
			return
		case constants.ProviderAmazon:
			s.HandleBoseAmazonToken(w, r)
			return
		}
	}

	log.Printf("[OAuth] Unknown music provider: %s", sanitizeLog(sourceID))
	http.Error(w, "Unknown music provider", http.StatusNotFound)
}

// HandleBoseLegacyToken handles the Bose-specific token refresh request (legacy or variant).
// POST /oauth/device/{deviceID}/music/musicprovider/{sourceID}/token
func (s *Server) HandleBoseLegacyToken(w http.ResponseWriter, r *http.Request) {
	s.HandleBoseToken(w, r)
}

// HandleBoseAccountToken handles the Bose-specific token refresh/exchange request from the app.
// POST /oauth/account/{account}/music/musicprovider/{sourceID}/token/cs
func (s *Server) HandleBoseAccountToken(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "sourceID")
	if sourceID != strconv.Itoa(constants.SpotifyProviderID) {
		http.Error(w, "Unknown music provider", http.StatusNotFound)
		return
	}

	// Local Marge accounts currently have no authenticated login identity:
	// Stockholm's compatibility login returns a synthetic account token. Never
	// turn that token into a real provider bearer. Account linking remains on
	// the Basic-Auth-protected management OAuth flow; speakers refresh through
	// the exact device-bound route below.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Deprecation", "true")
	http.Error(w, "Spotify account token exchange is unavailable; use the management authorization flow", http.StatusGone)
}

type spotifyTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
}

func (r spotifyTokenRequest) secret() string {
	if r.RefreshToken != "" {
		return r.RefreshToken
	}

	return r.Code
}

func readSpotifyTokenRequest(r *http.Request) (spotifyTokenRequest, error) {
	defer func() { _ = r.Body.Close() }()

	const maxSpotifyTokenRequestBytes = 64 << 10

	body, err := io.ReadAll(io.LimitReader(r.Body, maxSpotifyTokenRequestBytes+1))
	if err != nil {
		return spotifyTokenRequest{}, err
	}
	if len(body) > maxSpotifyTokenRequestBytes {
		return spotifyTokenRequest{}, fmt.Errorf("Spotify token request exceeds %d bytes", maxSpotifyTokenRequestBytes)
	}

	var request spotifyTokenRequest
	if len(body) != 0 {
		if err := json.Unmarshal(body, &request); err != nil {
			return spotifyTokenRequest{}, err
		}
	}

	return request, nil
}

// HandleBoseAmazonToken handles the Amazon Music token refresh request from the speaker.
// POST /oauth/device/{deviceID}/music/musicprovider/20/token/cs1
// The speaker sends the bare refresh token extracted from the stored AmazonSecret JSON.
func (s *Server) HandleBoseAmazonToken(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "deviceID")
	log.Printf("[Amazon] Token request for device %s", sanitizeLog(deviceID))

	s.mu.RLock()
	svc := s.amazonService
	s.mu.RUnlock()

	if svc == nil {
		log.Printf("[Amazon] Amazon service not configured, returning 503")
		http.Error(w, "Amazon service not configured", http.StatusServiceUnavailable)

		return
	}

	accounts := svc.GetAccounts()
	if len(accounts) == 0 {
		log.Printf("[Amazon] No Amazon accounts linked, returning 503")
		http.Error(w, "No linked Amazon accounts", http.StatusServiceUnavailable)

		return
	}

	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	var tokenReq struct {
		RefreshToken string `json:"refresh_token"`
		GrantType    string `json:"grant_type"`
		Code         string `json:"code"`
	}

	_ = json.Unmarshal(body, &tokenReq)

	// The speaker extracts the bare refresh token from AmazonSecret JSON and sends it here.
	secret := tokenReq.RefreshToken
	if secret == "" {
		secret = tokenReq.Code
	}

	var (
		account     *amazon.Account
		accessToken string
		userID      string
	)

	if secret != "" {
		if acc, ok := svc.GetAccountByRefreshToken(secret); ok {
			account = acc
			log.Printf("[Amazon] Found account for refresh token: %s", sanitizeLog(acc.UserID))
		}
	}

	if account != nil {
		if err := svc.RefreshAccessToken(account); err != nil {
			log.Printf("[Amazon] Failed to refresh token for %s: %v. Returning 502", sanitizeLog(account.UserID), err)
			http.Error(w, "Token refresh failed", http.StatusBadGateway)

			return
		}

		accessToken = account.AccessToken
	} else {
		var err error

		accessToken, userID, err = svc.GetFreshToken()
		if err != nil {
			log.Printf("[Amazon] Failed to get fresh token: %v. Returning 502", err)
			http.Error(w, "Failed to get fresh token", http.StatusBadGateway)

			return
		}

		log.Printf("[Amazon] Using default account %s", sanitizeLog(userID))
	}

	// Omit "scope" — Amazon Music scopes are undocumented; sending invented values
	// risks firmware rejection.
	response := map[string]interface{}{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Proxy-Origin", "self")
	w.Header().Set("Cache-Control", "no-store")

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[Amazon] Failed to encode response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// HandleBoseSpotifyToken handles the Bose-specific Spotify token refresh request.
// POST /oauth/device/{deviceID}/music/musicprovider/15/token/cs3
func (s *Server) HandleBoseSpotifyToken(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "deviceID")
	log.Printf("[Spotify Proxy] Token request for device %s", sanitizeLog(deviceID))
	if !isTrustedSpotifyOAuthClient(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()

	if svc == nil {
		log.Printf("[Spotify Proxy] Spotify service not configured, returning 503")
		http.Error(w, "Spotify service not configured", http.StatusServiceUnavailable)

		return
	}

	tokenReq, err := readSpotifyTokenRequest(r)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	binding, err := s.spotifyBindingForDevice(deviceID)
	if err != nil {
		log.Printf("[Spotify Proxy] Device ownership resolution failed for %s: %s", sanitizeLog(deviceID), sanitizeErr(err))
		http.Error(w, "Spotify account binding unavailable", http.StatusConflict)
		return
	}
	if err := validateSpotifyDeviceClient(r, binding); err != nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	linked, err := s.validateSpotifyBinding(binding, tokenReq.secret())
	if err != nil {
		http.Error(w, "Invalid Spotify credential", http.StatusUnauthorized)
		return
	}

	s.writeSpotifyAccessToken(w, svc, linked.UserID)
}

func (s *Server) writeSpotifyAccessToken(w http.ResponseWriter, svc *spotify.Service, userID string) {
	accessToken, _, err := svc.GetFreshTokenForUser(userID)
	if err != nil {
		log.Printf("[Spotify Proxy] Token unavailable for user %s: %s", sanitizeLog(userID), sanitizeErr(err))
		http.Error(w, "Failed to get fresh token", http.StatusBadGateway)
		return
	}

	// Format response as expected by Bose firmware.
	// Based on observed interactions, it's a JSON object with access_token.
	// The "scope" and other fields might be needed by some firmware versions.
	response := map[string]interface{}{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
		// These scopes are typical for what Bose requests.
		"scope": "playlist-read-private playlist-read-collaborative streaming user-library-read user-library-modify playlist-modify-private playlist-modify-public user-read-email user-read-private user-top-read",
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Proxy-Origin", "self")
	w.Header().Set("Cache-Control", "no-store")

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[Spotify Proxy] Failed to encode response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
