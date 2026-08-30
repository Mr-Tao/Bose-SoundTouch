package handlers

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/spotify"
)

var (
	errSpotifyBindingNotFound  = errors.New("Spotify account binding not found")
	errSpotifyBindingAmbiguous = errors.New("Spotify account binding is ambiguous")
	errSpotifySecretMismatch   = errors.New("Spotify surrogate secret mismatch")
	errSpotifyClientMismatch   = errors.New("Spotify token client does not match the bound device")
)

type spotifyBinding struct {
	MargeAccountID string
	DeviceID       string
	DeviceIP       string
	UserID         string
	Secret         string
	DisplayName    string
}

func isTrustedSpotifyOAuthClient(r *http.Request) bool {
	ip := net.ParseIP(clientHost(r))

	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func isSpotifyConfiguredSource(source models.ConfiguredSource) bool {
	typ := source.SourceKey.Type
	if typ == "" {
		typ = source.SourceKeyType
	}

	return strings.EqualFold(typ, constants.ProviderSpotify) ||
		strings.EqualFold(source.Type, constants.ProviderSpotify) ||
		source.SourceProviderID == strconv.Itoa(constants.SpotifyProviderID)
}

func bindingFromSources(accountID, deviceID string, sources []models.ConfiguredSource) (spotifyBinding, error) {
	bindings := make(map[string]spotifyBinding)
	foundSpotify := false

	for _, source := range sources {
		if !isSpotifyConfiguredSource(source) {
			continue
		}
		foundSpotify = true

		userID := source.SourceKey.Account
		if userID == "" {
			userID = source.SourceKeyAccount
		}
		if userID == "" {
			userID = source.Username
		}
		if userID == "" || source.Secret == "" {
			continue
		}

		binding := spotifyBinding{
			MargeAccountID: accountID,
			DeviceID:       deviceID,
			UserID:         userID,
			Secret:         source.Secret,
			DisplayName:    source.DisplayName,
		}
		bindings[userID+"\x00"+source.Secret] = binding
	}

	if len(bindings) == 0 {
		if foundSpotify {
			return spotifyBinding{}, fmt.Errorf("%w: configured source is incomplete", errSpotifyBindingNotFound)
		}

		return spotifyBinding{}, errSpotifyBindingNotFound
	}
	if len(bindings) != 1 {
		return spotifyBinding{}, errSpotifyBindingAmbiguous
	}

	for _, binding := range bindings {
		return binding, nil
	}

	return spotifyBinding{}, errSpotifyBindingNotFound
}

func (s *Server) spotifyBindingForDevice(deviceID string) (spotifyBinding, error) {
	if deviceID == "" {
		return spotifyBinding{}, errSpotifyBindingNotFound
	}

	accounts := s.ds.AllAccountsForDevice(deviceID)
	realAccounts := make([]string, 0, len(accounts))
	for _, accountID := range accounts {
		if accountID != "default" {
			realAccounts = append(realAccounts, accountID)
		}
	}
	if len(realAccounts) > 1 {
		return spotifyBinding{}, errSpotifyBindingAmbiguous
	}
	if len(realAccounts) == 1 {
		accounts = realAccounts
	}
	if len(accounts) != 1 {
		return spotifyBinding{}, errSpotifyBindingNotFound
	}
	accountID := accounts[0]
	match, err := s.ds.GetDeviceInfo(accountID, deviceID)
	if err != nil {
		return spotifyBinding{}, fmt.Errorf("read device info: %w", err)
	}
	if match == nil || match.AccountID == "" || match.DeviceID == "" ||
		!strings.EqualFold(match.DeviceID, deviceID) {
		return spotifyBinding{}, errSpotifyBindingNotFound
	}

	sources, err := s.ds.GetConfiguredSources(match.AccountID, match.DeviceID)
	if err != nil {
		return spotifyBinding{}, fmt.Errorf("read configured sources: %w", err)
	}

	binding, err := bindingFromSources(match.AccountID, match.DeviceID, sources)
	if err != nil {
		return spotifyBinding{}, err
	}
	binding.DeviceIP = match.IPAddress

	return binding, nil
}

func validateSpotifyDeviceClient(r *http.Request, binding spotifyBinding) error {
	clientIP := net.ParseIP(clientHost(r))
	deviceHost := strings.TrimSpace(binding.DeviceIP)
	if host, _, err := net.SplitHostPort(deviceHost); err == nil {
		deviceHost = host
	}
	deviceIP := net.ParseIP(deviceHost)
	if clientIP == nil || deviceIP == nil || !clientIP.Equal(deviceIP) ||
		!(deviceIP.IsPrivate() || deviceIP.IsLoopback() || deviceIP.IsLinkLocalUnicast()) {
		return errSpotifyClientMismatch
	}

	return nil
}

func (s *Server) validateSpotifyBinding(binding spotifyBinding, suppliedSecret string) (spotify.LinkedAccount, error) {
	if suppliedSecret == "" || subtle.ConstantTimeCompare([]byte(binding.Secret), []byte(suppliedSecret)) != 1 {
		return spotify.LinkedAccount{}, errSpotifySecretMismatch
	}

	s.mu.RLock()
	svc := s.spotifyService
	s.mu.RUnlock()
	if svc == nil {
		return spotify.LinkedAccount{}, fmt.Errorf("Spotify service unavailable")
	}

	linked, ok := svc.GetLinkedAccount(binding.UserID)
	if !ok {
		if err := svc.AdoptBoseSecret(binding.UserID, binding.Secret); err == nil {
			linked, ok = svc.GetLinkedAccount(binding.UserID)
		}
	}
	if !ok || subtle.ConstantTimeCompare([]byte(linked.BoseSecret), []byte(binding.Secret)) != 1 {
		return spotify.LinkedAccount{}, errSpotifySecretMismatch
	}

	return linked, nil
}
