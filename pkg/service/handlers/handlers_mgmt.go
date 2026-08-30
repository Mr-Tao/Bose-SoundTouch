package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/marge"
	"github.com/gesellix/bose-soundtouch/pkg/service/spotify"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// BasicAuthMgmt returns a Basic Auth middleware using the server's management credentials.
func (s *Server) BasicAuthMgmt() func(http.Handler) http.Handler {
	s.mu.RLock()
	username := s.mgmtUsername
	password := s.mgmtPassword
	s.mu.RUnlock()

	return middleware.BasicAuth("Management API", map[string]string{username: password})
}

// BasicAuthAdmin returns a middleware gating the whole admin area (/admin,
// /setup, /api/setup — minus the small set of routes shared with
// soundtouch-cli/soundtouch-player) behind the same credentials as
// BasicAuthMgmt. Unlike BasicAuthMgmt, which captures username/password once
// at router-setup time (main.go builds the router once at startup),
// BasicAuthAdmin reads the live AdminAreaAuth mode and credentials on every
// request, so toggling the setting via the Settings UI (HandleUpdateSettings
// -> SetAdminAreaAuth) takes effect immediately — no restart. When the mode
// isn't "enabled", every request passes through unauthenticated, i.e.
// today's default behavior. See #419 and
// _/i419/design-admin-area-auth-gate.md.
func (s *Server) BasicAuthAdmin() func(http.Handler) http.Handler {
	const realm = "Admin Area"

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.mu.RLock()
			mode := s.adminAreaAuth
			username := s.mgmtUsername
			password := s.mgmtPassword
			s.mu.RUnlock()

			if mode != "enabled" {
				next.ServeHTTP(w, r)
				return
			}

			user, pass, ok := r.BasicAuth()
			if !ok || user != username || subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, realm))
				w.WriteHeader(http.StatusUnauthorized)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// HandleMgmtListSpeakers returns discovered speakers for the given account.
func (s *Server) HandleMgmtListSpeakers(w http.ResponseWriter, r *http.Request) {
	_ = chi.URLParam(r, "accountId")

	allDevices, err := s.ds.ListAllDevices()
	if err != nil {
		log.Printf("[Mgmt] Failed to list devices: %v", err)

		allDevices = nil
	}

	type speaker struct {
		IPAddress string `json:"ipAddress"`
		Name      string `json:"name"`
		DeviceID  string `json:"deviceId"`
		Type      string `json:"type"`
	}

	speakers := make([]speaker, 0, len(allDevices))
	for i := range allDevices {
		d := &allDevices[i]
		speakers = append(speakers, speaker{
			IPAddress: d.IPAddress,
			Name:      d.Name,
			DeviceID:  d.DeviceID,
			Type:      d.ProductCode,
		})
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"speakers": speakers,
	}); err != nil {
		log.Printf("[Mgmt] Failed to encode speakers: %v", err)
	}
}

// HandleMgmtDeviceEvents returns events for a device (currently a placeholder).
func (s *Server) HandleMgmtDeviceEvents(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "deviceId")

	events := s.ds.GetDeviceEvents(deviceID)
	if events == nil {
		events = nil // will marshal as empty array via wrapper
	}

	w.Header().Set("Content-Type", "application/json")
	// Return the events in the structure the Flutter app expects.
	// Use an explicit empty slice to ensure JSON "[]" instead of "null".
	type eventEntry struct {
		Type string                 `json:"type"`
		Time string                 `json:"time"`
		Data map[string]interface{} `json:"data"`
	}

	result := make([]eventEntry, 0, len(events))
	for _, e := range events {
		result = append(result, eventEntry{
			Type: e.Type,
			Time: e.Time,
			Data: e.Data,
		})
	}

	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"events": result,
	}); err != nil {
		log.Printf("[Mgmt] Failed to encode events: %v", err)
	}
}

// HandleMgmtSpotifyInit starts the Spotify OAuth flow by returning an authorization URL.
func (s *Server) HandleMgmtSpotifyInit(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()

	if svc == nil {
		http.Error(w, `{"error":"spotify not configured"}`, http.StatusServiceUnavailable)
		return
	}
	clientID, clientSecret, _ := s.GetSpotifyConfig()
	if err := ValidateSpotifyAuthorizationConfig(clientID, clientSecret, s.EffectiveSpotifyRedirectURI()); err != nil {
		log.Printf("[Mgmt] Spotify authorization preflight failed: %s", sanitizeErr(err))
		http.Error(w, `{"error":"spotify configuration is incomplete or invalid"}`, http.StatusPreconditionFailed)
		return
	}

	accountID := r.URL.Query().Get("account")
	state, session, err := s.newSpotifyOAuthTransaction(accountID)
	if err != nil {
		log.Printf("[Mgmt] Spotify authorization could not start: %s", sanitizeErr(err))
		http.Error(w, `{"error":"valid explicit Marge account required"}`, http.StatusBadRequest)
		return
	}
	redirectURL, err := spotifyOAuthBootstrapURL(s.EffectiveSpotifyRedirectURI(), state, session)
	if err != nil {
		log.Printf("[Mgmt] Spotify authorization bootstrap URL failed: %s", sanitizeErr(err))
		http.Error(w, `{"error":"spotify callback configuration is invalid"}`, http.StatusPreconditionFailed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(map[string]string{
		"redirectUrl": redirectURL,
	}); err != nil {
		log.Printf("[Mgmt] Failed to encode redirect URL: %v", err)
	}
}

func spotifyOAuthBootstrapURL(callbackURI, state, session string) (string, error) {
	parsed, err := url.Parse(callbackURI)
	if err != nil {
		return "", err
	}
	parsed.Path = "/mgmt/spotify/start"
	parsed.RawPath = ""
	parsed.RawQuery = url.Values{"state": {state}, "session": {session}}.Encode()
	parsed.Fragment = ""

	return parsed.String(), nil
}

func spotifyOAuthCookieName(state string) string {
	if len(state) > 16 {
		state = state[:16]
	}

	return "aftertouch_spotify_oauth_" + state
}

func setSpotifyOAuthCookie(w http.ResponseWriter, state, session string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     spotifyOAuthCookieName(state),
		Value:    session,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// HandleMgmtSpotifyStart establishes the browser-bound OAuth transaction on
// the configured callback origin, then redirects to Spotify. The bootstrap URL
// is returned only by the authenticated init endpoint and is single-use.
func (s *Server) HandleMgmtSpotifyStart(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()
	if svc == nil {
		http.Error(w, "Spotify integration not configured", http.StatusServiceUnavailable)
		return
	}

	state := r.URL.Query().Get("state")
	session := r.URL.Query().Get("session")
	if err := s.bootstrapSpotifyOAuthTransaction(state, session); err != nil {
		http.Error(w, "Invalid or already used Spotify authorization bootstrap", http.StatusBadRequest)
		return
	}

	callbackURI, err := url.Parse(s.EffectiveSpotifyRedirectURI())
	if err != nil {
		http.Error(w, "Spotify callback configuration is invalid", http.StatusPreconditionFailed)
		return
	}
	setSpotifyOAuthCookie(w, state, session, callbackURI.Scheme == "https", int(s.spotifyOAuthTTL.Seconds()))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, svc.BuildAuthorizeURL(state), http.StatusSeeOther)
}

func (s *Server) consumeSpotifyOAuthBrowserTransaction(w http.ResponseWriter, r *http.Request) (spotifyOAuthTransaction, error) {
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(spotifyOAuthCookieName(state))
	if err != nil {
		return spotifyOAuthTransaction{}, errSpotifyOAuthSession
	}
	transaction, err := s.consumeSpotifyOAuthTransaction(state, cookie.Value)
	if err != nil {
		return spotifyOAuthTransaction{}, err
	}
	setSpotifyOAuthCookie(w, state, "", cookie.Secure, -1)

	return transaction, nil
}

func (s *Server) exchangeAndPublishSpotifyAuthorization(svc *spotify.Service, transaction spotifyOAuthTransaction, code string) (spotify.LinkedAccount, spotifySourcePublicationResult, error) {
	exchange, err := svc.ExchangeCode(code)
	if err != nil {
		return spotify.LinkedAccount{}, spotifySourcePublicationResult{}, err
	}

	return s.commitAndPublishSpotifyAuthorization(svc, transaction, exchange)
}

func (s *Server) commitAndPublishSpotifyAuthorization(svc *spotify.Service, transaction spotifyOAuthTransaction, exchange spotify.AuthorizationExchange) (spotify.LinkedAccount, spotifySourcePublicationResult, error) {
	// Lock order is spotifySourceMu -> spotifyOAuthMu or spotify.Service.mu.
	// Provider HTTP exchange has already completed. Keep durable account commit
	// and source publication in one ownership epoch so a newer intent can win
	// before both operations or after both, never between them.
	s.spotifySourceMu.Lock()
	if !s.spotifyOAuthPublicationCurrent(transaction) {
		s.spotifySourceMu.Unlock()
		return spotify.LinkedAccount{}, spotifySourcePublicationResult{}, errSpotifyOAuthSuperseded
	}

	linked, err := svc.StoreAuthorizationExchange(exchange)
	if err != nil {
		s.spotifySourceMu.Unlock()
		return spotify.LinkedAccount{}, spotifySourcePublicationResult{}, err
	}
	if s.spotifyOAuthAfterStore != nil {
		s.spotifyOAuthAfterStore()
	}
	publication, err := s.bridgeSpotifyToMargeLocked(transaction, linked)
	s.spotifySourceMu.Unlock()
	if err == nil {
		s.notifyDevicesChanged()
	}

	return linked, publication, err
}

// HandleMgmtSpotifyCallback is the browser OAuth callback from Spotify.
// Not protected by Basic Auth — Spotify redirects the user's browser here directly.
// Returns an HTML page the user can close.
func (s *Server) HandleMgmtSpotifyCallback(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()

	if svc == nil {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`<html><body><h1>Error</h1><p>Spotify integration not configured</p></body></html>`))

		return
	}

	transaction, err := s.consumeSpotifyOAuthBrowserTransaction(w, r)
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<html><body><h1>Spotify Authorization Failed</h1><p>Invalid, expired, or already used authorization state.</p></body></html>`))
		return
	}

	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		// html.EscapeString neutralises any HTML metacharacters in the
		// caller-supplied error string before it lands in the response.
		_, _ = w.Write([]byte(`<html><body><h1>Spotify Authorization Failed</h1><p>Error: ` + html.EscapeString(errMsg) + `</p></body></html>`))

		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<html><body><h1>Missing authorization code</h1></body></html>`))

		return
	}

	linked, publication, err := s.exchangeAndPublishSpotifyAuthorization(svc, transaction, code)
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		if linked.UserID == "" {
			log.Printf("[Mgmt] Spotify callback failed: %v", err)
			_, _ = w.Write([]byte(`<html><body><h1>Error</h1><p>Token exchange failed</p></body></html>`))
		} else {
			log.Printf("[Mgmt] Spotify callback bridge failed: %s", sanitizeErr(err))
			_, _ = w.Write([]byte(`<html><body><h1>Error</h1><p>Spotify account was linked, but source registration failed.</p></body></html>`))
		}
		return
	}

	w.Header().Set("Content-Type", "text/html")
	if publication.Pending != 0 || publication.Unverified != 0 {
		count := publication.Pending + publication.Unverified
		_, _ = w.Write([]byte(`<html><body><h1>Spotify Connected</h1><p>The source was stored, but publication remains unverified on ` + strconv.Itoa(count) + ` speaker(s).</p><p>You can close this window.</p></body></html>`))
		return
	}
	_, _ = w.Write([]byte(`<html><body><h1>Spotify Connected</h1><p>The source was stored and published to ` + strconv.Itoa(publication.Confirmed) + ` speaker(s). You can close this window.</p></body></html>`))
}

// HandleMgmtSpotifyConfirm is the authenticated compatibility completion path
// for an already bootstrapped browser transaction. The matching browser cookie
// is still required; this is not a standalone native-app deep-link flow.
func (s *Server) HandleMgmtSpotifyConfirm(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()

	if svc == nil {
		http.Error(w, `{"error":"spotify not configured"}`, http.StatusServiceUnavailable)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, `{"error":"missing code parameter"}`, http.StatusBadRequest)
		return
	}

	transaction, err := s.consumeSpotifyOAuthBrowserTransaction(w, r)
	if err != nil {
		http.Error(w, `{"error":"invalid, expired, or already used state"}`, http.StatusBadRequest)
		return
	}

	linked, publication, err := s.exchangeAndPublishSpotifyAuthorization(svc, transaction, code)
	if err != nil {
		if linked.UserID == "" {
			log.Printf("[Mgmt] Spotify confirm failed: %v", err)
			http.Error(w, `{"error":"token exchange failed"}`, http.StatusInternalServerError)
		} else {
			log.Printf("[Mgmt] Spotify confirm bridge failed: %s", sanitizeErr(err))
			http.Error(w, `{"error":"source registration failed"}`, http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":          true,
		"publication": publication,
	}); err != nil {
		log.Printf("[Mgmt] Failed to encode Spotify publication result: %v", err)
	}
}

type spotifySourcePublicationResult struct {
	Confirmed  int `json:"confirmed"`
	Pending    int `json:"pending"`
	Unverified int `json:"unverified"`
}

func (s *Server) bridgeSpotifyToMarge(transaction spotifyOAuthTransaction, account spotify.LinkedAccount) (spotifySourcePublicationResult, error) {
	s.spotifySourceMu.Lock()
	result, err := s.bridgeSpotifyToMargeLocked(transaction, account)
	s.spotifySourceMu.Unlock()
	if err == nil {
		s.notifyDevicesChanged()
	}

	return result, err
}

// bridgeSpotifyToMargeLocked performs datastore and bounded speaker
// publication while the caller owns spotifySourceMu. Observer notification is
// the caller's responsibility after releasing the ownership lock.
func (s *Server) bridgeSpotifyToMargeLocked(transaction spotifyOAuthTransaction, account spotify.LinkedAccount) (spotifySourcePublicationResult, error) {
	accountID := transaction.MargeAccountID
	result := spotifySourcePublicationResult{}
	if accountID == "" || accountID == "default" || account.UserID == "" || account.BoseSecret == "" {
		return result, fmt.Errorf("incomplete account binding")
	}
	if !s.hasMargeAccount(accountID) {
		return result, fmt.Errorf("Marge account no longer exists")
	}

	if !s.spotifyOAuthPublicationCurrent(transaction) {
		return result, fmt.Errorf("%w before source publication", errSpotifyOAuthSuperseded)
	}
	if !s.spotifyLinkedIdentityCurrent(account) {
		return result, fmt.Errorf("Spotify identity changed before source publication")
	}

	log.Printf("[Spotify Bridge] Registering Spotify user %s in Marge for account %s", sanitizeLog(account.UserID), sanitizeLog(accountID))

	_, err := marge.AddSource(s.ds, accountID, account.UserID, strconv.Itoa(constants.SpotifyProviderID), account.BoseSecret, constants.CredentialTypeTokenV3, account.DisplayName)
	if err != nil {
		return result, fmt.Errorf("register source in Marge: %w", err)
	}

	allDevices, err := s.ds.ListAllDevices()
	if err != nil {
		return result, fmt.Errorf("list devices: %w", err)
	}
	if !s.spotifyLinkedIdentityCurrent(account) {
		return result, fmt.Errorf("Spotify identity changed during source publication")
	}

	type publicationResult struct {
		device models.ServiceDeviceInfo
		err    error
	}
	results := make(chan publicationResult, len(allDevices))
	pending := 0
	for i := range allDevices {
		dev := &allDevices[i]
		if dev.AccountID != accountID {
			continue
		}

		if dev.IPAddress == "" {
			log.Printf("[Spotify Bridge] Speaker %s has no address; source inventory remains pending", sanitizeLog(dev.Name))
			result.Pending++
			continue
		}

		pending++
		go func(d models.ServiceDeviceInfo) {
			results <- publicationResult{device: d, err: s.publishSpotifyAccountToSpeaker(d, account)}
		}(*dev)
	}

	for i := 0; i < pending; i++ {
		publication := <-results
		if publication.err != nil {
			log.Printf("[Spotify Bridge] Speaker %s source publication remains unverified: %v", sanitizeLog(publication.device.Name), publication.err)
			result.Unverified++
		} else {
			log.Printf("[Spotify Bridge] Speaker %s confirmed the exact ready Spotify source", sanitizeLog(publication.device.Name))
			result.Confirmed++
		}
	}
	if !s.spotifyLinkedIdentityCurrent(account) {
		return result, fmt.Errorf("Spotify identity changed before source publication completed")
	}

	return result, nil
}

func (s *Server) spotifyLinkedIdentityCurrent(expected spotify.LinkedAccount) bool {
	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()
	if svc == nil {
		return false
	}
	current, ok := svc.GetLinkedAccount(expected.UserID)

	return ok && current.BoseSecret == expected.BoseSecret
}

func (s *Server) spotifyLinkedAccountCurrent(expected spotify.LinkedAccount) bool {
	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()
	if svc == nil {
		return false
	}
	current, ok := svc.GetLinkedAccount(expected.UserID)

	return ok && current.BoseSecret == expected.BoseSecret && current.Generation == expected.Generation
}

func (s *Server) publishSpotifyAccountToSpeaker(device models.ServiceDeviceInfo, account spotify.LinkedAccount) error {
	if !s.spotifyPublicationBindingCurrent(device, account) {
		return fmt.Errorf("Marge Spotify credential binding changed before speaker publication")
	}

	cfg := client.DefaultConfig()
	cfg.Host = device.IPAddress
	cfg.Timeout = 5 * time.Second
	c := client.NewClient(cfg)
	creds := models.NewSpotifyOAuthCredentials(account.UserID, account.BoseSecret, account.DisplayName)
	if err := c.SetMusicServiceOAuthAccount(creds); err != nil {
		errs := &models.ErrorsResponse{}
		if !errors.As(err, &errs) {
			return err
		}
		unsupported := false
		for _, speakerErr := range errs.Errors {
			if speakerErr.Value == 1029 {
				unsupported = true
				break
			}
		}
		if !unsupported {
			return err
		}

		if notifyErr := c.NotifySourcesUpdated(device.DeviceID); notifyErr != nil {
			legacyCreds := models.NewSpotifyCredentials(account.UserID, account.BoseSecret)
			if legacyErr := c.SetMusicServiceAccount(legacyCreds); legacyErr != nil {
				return fmt.Errorf("OAuth, source notification, and legacy publication failed: %w", legacyErr)
			}
		}
	}

	sources, err := c.GetSources()
	if err != nil {
		return fmt.Errorf("read back speaker sources: %w", err)
	}
	for _, source := range sources.SourceItem {
		if source.IsSpotify() && source.Status.IsReady() && source.SourceAccount == account.UserID {
			if !s.spotifyPublicationBindingCurrent(device, account) {
				return fmt.Errorf("Marge Spotify credential binding changed during speaker publication")
			}
			return nil
		}
	}

	return fmt.Errorf("speaker did not report an exact READY Spotify source for account %s", account.UserID)
}

func (s *Server) spotifyPublicationBindingCurrent(device models.ServiceDeviceInfo, account spotify.LinkedAccount) bool {
	if device.AccountID == "" || device.DeviceID == "" {
		return false
	}
	sources, err := s.ds.GetConfiguredSources(device.AccountID, device.DeviceID)
	if err != nil {
		return false
	}
	binding, err := bindingFromSources(device.AccountID, device.DeviceID, sources)

	return err == nil && binding.UserID == account.UserID && binding.Secret == account.BoseSecret
}

// HandleMgmtSpotifyAccounts returns linked Spotify accounts (tokens stripped).
func (s *Server) HandleMgmtSpotifyAccounts(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()

	if svc == nil {
		http.Error(w, `{"error":"spotify not configured"}`, http.StatusServiceUnavailable)
		return
	}

	accounts := svc.GetAccounts()

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"accounts": accounts,
	}); err != nil {
		log.Printf("[Mgmt] Failed to encode accounts: %v", err)
	}
}

// HandleMgmtSpotifyToken is retained as a tombstone for older management
// clients. Access tokens are brokered only to an authenticated speaker route;
// management clients must request a bounded prime operation instead.
func (s *Server) HandleMgmtSpotifyToken(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Warning", `299 - "Spotify token export was removed; use the prime endpoint"`)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, `{"error":"Spotify token export removed; use /api/mgmt/spotify/prime"}`, http.StatusGone)
}

// HandleMgmtSpotifyEntity resolves a Spotify URI to name and image URL.
func (s *Server) HandleMgmtSpotifyEntity(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()

	if svc == nil {
		http.Error(w, `{"error":"spotify not configured"}`, http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to read body"}`, http.StatusBadRequest)
		return
	}

	var request struct {
		URI     string `json:"uri"`
		Account string `json:"account"`
	}
	if unmarshalErr := json.Unmarshal(body, &request); unmarshalErr != nil || request.URI == "" || request.Account == "" {
		http.Error(w, `{"error":"explicit Spotify account and valid uri required"}`, http.StatusBadRequest)
		return
	}

	name, imageURL, err := svc.ResolveEntityForUser(request.Account, request.URI)
	if err != nil {
		log.Printf("[Mgmt] Spotify entity resolve error: %s", sanitizeErr(err))
		http.Error(w, `{"error":"entity resolution failed"}`, http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]string{
		"name":     name,
		"imageUrl": imageURL,
	}); err != nil {
		log.Printf("[Mgmt] Failed to encode entity: %v", err)
	}
}

// HandleMgmtPrimeDevice triggers a Spotify priming for a specific device.
func (s *Server) HandleMgmtPrimeDevice(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("deviceId")

	if deviceID == "" {
		http.Error(w, `{"error":"missing deviceId"}`, http.StatusBadRequest)
		return
	}

	deviceIP, err := s.resolveDeviceIDToIP(deviceID)
	if err != nil {
		log.Printf("[Mgmt] Prime failed: %s", sanitizeErr(err))
		http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	result := s.PrimeDeviceWithSpotify(deviceIP)
	status := http.StatusOK
	if result.Outcome != "confirmed" {
		status = http.StatusConflict
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("[Mgmt] Failed to encode Spotify priming result: %v", err)
	}
}

// HandleMgmtAmazonInit starts the Amazon OAuth flow by returning an authorization URL.
func (s *Server) HandleMgmtAmazonInit(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.amazonService
	s.mu.RUnlock()

	if svc == nil {
		http.Error(w, `{"error":"amazon not configured"}`, http.StatusServiceUnavailable)
		return
	}

	state := r.URL.Query().Get("account")
	redirectURL := svc.BuildAuthorizeURL(state)

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(map[string]string{
		"redirectUrl": redirectURL,
	}); err != nil {
		log.Printf("[Mgmt] Failed to encode Amazon redirect URL: %v", err)
	}
}

// HandleMgmtAmazonCallback is the browser OAuth callback from Amazon LWA.
// Not protected by Basic Auth — Amazon redirects the user's browser here directly.
// Returns an HTML page the user can close.
func (s *Server) HandleMgmtAmazonCallback(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.amazonService
	s.mu.RUnlock()

	if svc == nil {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`<html><body><h1>Error</h1><p>Amazon Music integration not configured</p></body></html>`))

		return
	}

	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		// html.EscapeString neutralises any HTML metacharacters in the
		// caller-supplied error string before it lands in the response.
		_, _ = w.Write([]byte(`<html><body><h1>Amazon Authorization Failed</h1><p>Error: ` + html.EscapeString(errMsg) + `</p></body></html>`))

		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<html><body><h1>Missing authorization code</h1></body></html>`))

		return
	}

	if err := svc.ExchangeCodeAndStore(code); err != nil {
		log.Printf("[Mgmt] Amazon callback failed: %v", err)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<html><body><h1>Error</h1><p>Token exchange failed</p></body></html>`))

		return
	}

	accountID := r.URL.Query().Get("account")
	if accountID == "" {
		accountID = r.URL.Query().Get("state")
	}

	s.bridgeAmazonToMarge(accountID)

	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte(`<html><body><h1>Amazon Music Connected</h1><p>You can close this window.</p></body></html>`))
}

// HandleMgmtAmazonConfirm exchanges an authorization code for tokens.
// Used by the ueberboese mobile app after the deep link callback delivers the code.
// Protected by Basic Auth.
func (s *Server) HandleMgmtAmazonConfirm(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	svc := s.amazonService
	s.mu.RUnlock()

	if svc == nil {
		http.Error(w, `{"error":"amazon not configured"}`, http.StatusServiceUnavailable)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, `{"error":"missing code parameter"}`, http.StatusBadRequest)
		return
	}

	if err := svc.ExchangeCodeAndStore(code); err != nil {
		log.Printf("[Mgmt] Amazon confirm failed: %v", err)
		http.Error(w, `{"error":"token exchange failed"}`, http.StatusInternalServerError)

		return
	}

	accountID := r.URL.Query().Get("account")
	if accountID == "" {
		accountID = r.URL.Query().Get("state")
	}

	s.bridgeAmazonToMarge(accountID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) bridgeAmazonToMarge(accountID string) {
	if accountID == "" {
		accountID = "default"
	}

	s.mu.RLock()
	svc := s.amazonService
	s.mu.RUnlock()

	if svc == nil {
		return
	}

	accounts := svc.GetAllAccounts()
	if len(accounts) == 0 {
		return
	}

	for _, acc := range accounts {
		log.Printf("[Amazon Bridge] Registering Amazon user %s in Marge for account %s", sanitizeLog(acc.Email), sanitizeLog(accountID))

		// Build the AmazonSecret credential envelope expected by the speaker firmware.
		credMap := map[string]interface{}{
			"AmazonSecret": map[string]string{
				"refresh_token": acc.RefreshToken,
				"site_id":       acc.SiteID,
			},
		}

		credJSON, err := json.Marshal(credMap)
		if err != nil {
			log.Printf("[Amazon Bridge] Failed to marshal credential: %v", err)
			continue
		}

		_, err = marge.AddSource(s.ds, accountID, acc.Email, strconv.Itoa(constants.AmazonProviderID), string(credJSON), constants.CredentialTypeToken, acc.DisplayName)
		if err != nil {
			log.Printf("[Amazon Bridge] Failed to register source in Marge: %v", err)
			continue
		}

		allDevices, err := s.ds.ListAllDevices()
		if err != nil {
			log.Printf("[Amazon Bridge] Failed to list devices: %v", err)
			continue
		}

		for i := range allDevices {
			dev := &allDevices[i]
			if dev.AccountID != accountID && accountID != "default" {
				continue
			}

			if dev.IPAddress == "" {
				continue
			}

			go func(d models.ServiceDeviceInfo) {
				log.Printf("[Amazon Bridge] Notifying speaker %s (%s) about new Amazon account", sanitizeLog(d.Name), sanitizeLog(d.IPAddress))

				cfg := client.DefaultConfig()
				cfg.Host = d.IPAddress
				cfg.Timeout = 5 * time.Second
				c := client.NewClient(cfg)
				creds := models.NewAmazonOAuthCredentials(acc.Email, string(credJSON), acc.DisplayName)

				if err := c.SetMusicServiceOAuthAccount(creds); err != nil {
					log.Printf("[Amazon Bridge] Failed to notify speaker %s via OAuth: %v", sanitizeLog(d.Name), err)
					log.Printf("[Amazon Bridge] Speaker %s doesn't support OAuth or is unreachable, falling back to Marge sync notification", sanitizeLog(d.Name))

					if err := c.NotifySourcesUpdated(d.DeviceID); err != nil {
						log.Printf("[Amazon Bridge] Sync notification failed for speaker %s: %v", sanitizeLog(d.Name), err)
						log.Printf("[Amazon Bridge] Falling back to legacy account creation for speaker %s", sanitizeLog(d.Name))

						legacyCreds := models.NewAmazonMusicCredentials(acc.Email, string(credJSON))
						if err := c.SetMusicServiceAccount(legacyCreds); err != nil {
							log.Printf("[Amazon Bridge] Legacy fallback failed for speaker %s: %v", sanitizeLog(d.Name), err)
						} else {
							log.Printf("[Amazon Bridge] Legacy fallback successful for speaker %s", sanitizeLog(d.Name))
						}
					} else {
						log.Printf("[Amazon Bridge] Sync notification successful for speaker %s", sanitizeLog(d.Name))
					}
				} else {
					log.Printf("[Amazon Bridge] Successfully notified speaker %s", sanitizeLog(d.Name))
				}
			}(*dev)
		}
	}
}

// HandleMgmtAmazonAccounts returns linked Amazon accounts (tokens stripped).
func (s *Server) HandleMgmtAmazonAccounts(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	svc := s.amazonService
	s.mu.RUnlock()

	if svc == nil {
		http.Error(w, `{"error":"amazon not configured"}`, http.StatusServiceUnavailable)
		return
	}

	accounts := svc.GetAccounts()

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"accounts": accounts,
	}); err != nil {
		log.Printf("[Mgmt] Failed to encode Amazon accounts: %v", err)
	}
}

// HandleMgmtAmazonToken returns a fresh Amazon access token for the linked account.
func (s *Server) HandleMgmtAmazonToken(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	svc := s.amazonService
	s.mu.RUnlock()

	if svc == nil {
		http.Error(w, `{"error":"amazon not configured"}`, http.StatusServiceUnavailable)
		return
	}

	accessToken, username, err := svc.GetFreshToken()
	if err != nil {
		log.Printf("[Mgmt] Amazon token error: %v", err)
		http.Error(w, `{"error":"no token available"}`, http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(map[string]string{
		"access_token": accessToken,
		"username":     username,
	}); err != nil {
		log.Printf("[Mgmt] Failed to encode Amazon token: %v", err)
	}
}

// HandleMgmtPrimeDeviceAmazon triggers Amazon Music priming for a specific device.
func (s *Server) HandleMgmtPrimeDeviceAmazon(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("deviceId")

	if deviceID == "" {
		http.Error(w, `{"error":"missing deviceId"}`, http.StatusBadRequest)
		return
	}

	deviceIP, err := s.resolveDeviceIDToIP(deviceID)
	if err != nil {
		log.Printf("[Mgmt] Amazon prime failed: %s", sanitizeErr(err))
		http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), http.StatusNotFound)

		return
	}

	go s.PrimeDeviceWithAmazon(deviceIP)

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"Priming triggered"}`))
}
