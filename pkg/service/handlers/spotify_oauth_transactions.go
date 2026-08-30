package handlers

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

var (
	errSpotifyOAuthStateInvalid = errors.New("spotify OAuth state is invalid or already used")
	errSpotifyOAuthStateExpired = errors.New("spotify OAuth state expired")
	errSpotifyOAuthSession      = errors.New("spotify OAuth browser session does not match")
	errSpotifyOAuthBootstrap    = errors.New("spotify OAuth browser bootstrap is invalid or already used")
	errSpotifyOAuthSuperseded   = errors.New("spotify OAuth transaction was superseded")
)

const maxSpotifyOAuthTransactions = 128

type spotifyOAuthTransaction struct {
	MargeAccountID        string
	PublicationGeneration uint64
	ExpiresAt             time.Time
	SessionHash           [sha256.Size]byte
	Bootstrapped          bool
}

func (s *Server) newSpotifyOAuthTransaction(accountID string) (state, session string, err error) {
	if !s.hasMargeAccount(accountID) {
		return "", "", fmt.Errorf("marge account %q not found", accountID)
	}

	random := make([]byte, 64)
	if _, err := io.ReadFull(s.spotifyOAuthRandom, random); err != nil {
		return "", "", fmt.Errorf("generate OAuth transaction: %w", err)
	}

	state = hex.EncodeToString(random[:32])
	session = hex.EncodeToString(random[32:])
	now := time.Now()

	// Serialize a new account-level authorization intent with source
	// publication. Once this generation is issued, an older callback can no
	// longer replace the account's configured Spotify source.
	s.spotifySourceMu.Lock()
	defer s.spotifySourceMu.Unlock()

	s.spotifyOAuthMu.Lock()
	defer s.spotifyOAuthMu.Unlock()

	s.pruneSpotifyOAuthTransactionsLocked(now)

	if len(s.spotifyOAuthTransactions) >= maxSpotifyOAuthTransactions {
		return "", "", fmt.Errorf("too many pending Spotify OAuth transactions")
	}

	if _, exists := s.spotifyOAuthTransactions[state]; exists {
		return "", "", fmt.Errorf("generated duplicate OAuth state")
	}

	generation := s.spotifyOAuthGenerations[accountID] + 1
	s.spotifyOAuthGenerations[accountID] = generation
	s.spotifyOAuthTransactions[state] = spotifyOAuthTransaction{
		MargeAccountID:        accountID,
		PublicationGeneration: generation,
		ExpiresAt:             now.Add(s.spotifyOAuthTTL),
		SessionHash:           sha256.Sum256([]byte(session)),
	}

	return state, session, nil
}

func (s *Server) bootstrapSpotifyOAuthTransaction(state, session string) error {
	if state == "" || session == "" {
		return errSpotifyOAuthBootstrap
	}

	now := time.Now()

	s.spotifyOAuthMu.Lock()
	defer s.spotifyOAuthMu.Unlock()

	transaction, ok := s.spotifyOAuthTransactions[state]
	s.pruneSpotifyOAuthTransactionsLocked(now)

	if !ok || !transaction.ExpiresAt.After(now) || transaction.Bootstrapped ||
		!spotifyOAuthSessionMatches(transaction, session) {
		return errSpotifyOAuthBootstrap
	}

	if !s.spotifyOAuthPublicationCurrentLocked(transaction) {
		delete(s.spotifyOAuthTransactions, state)
		return errSpotifyOAuthSuperseded
	}

	transaction.Bootstrapped = true
	s.spotifyOAuthTransactions[state] = transaction

	return nil
}

func (s *Server) consumeSpotifyOAuthTransaction(state, session string) (spotifyOAuthTransaction, error) {
	if state == "" || session == "" {
		return spotifyOAuthTransaction{}, errSpotifyOAuthStateInvalid
	}

	now := time.Now()

	s.spotifyOAuthMu.Lock()
	defer s.spotifyOAuthMu.Unlock()

	transaction, ok := s.spotifyOAuthTransactions[state]
	s.pruneSpotifyOAuthTransactionsLocked(now)

	if !ok {
		return spotifyOAuthTransaction{}, errSpotifyOAuthStateInvalid
	}

	if !transaction.ExpiresAt.After(now) {
		return spotifyOAuthTransaction{}, errSpotifyOAuthStateExpired
	}

	if !s.spotifyOAuthPublicationCurrentLocked(transaction) {
		delete(s.spotifyOAuthTransactions, state)
		return spotifyOAuthTransaction{}, errSpotifyOAuthSuperseded
	}

	if !transaction.Bootstrapped || !spotifyOAuthSessionMatches(transaction, session) {
		return spotifyOAuthTransaction{}, errSpotifyOAuthSession
	}

	delete(s.spotifyOAuthTransactions, state)

	return transaction, nil
}

func spotifyOAuthSessionMatches(transaction spotifyOAuthTransaction, session string) bool {
	hash := sha256.Sum256([]byte(session))

	return subtle.ConstantTimeCompare(transaction.SessionHash[:], hash[:]) == 1
}

func (s *Server) pruneSpotifyOAuthTransactionsLocked(now time.Time) {
	for state, transaction := range s.spotifyOAuthTransactions {
		if !transaction.ExpiresAt.After(now) {
			delete(s.spotifyOAuthTransactions, state)
		}
	}
}

func (s *Server) spotifyOAuthPublicationCurrent(transaction spotifyOAuthTransaction) bool {
	s.spotifyOAuthMu.Lock()
	defer s.spotifyOAuthMu.Unlock()

	return s.spotifyOAuthPublicationCurrentLocked(transaction)
}

func (s *Server) spotifyOAuthPublicationCurrentLocked(transaction spotifyOAuthTransaction) bool {
	return transaction.MargeAccountID != "" && transaction.PublicationGeneration != 0 &&
		s.spotifyOAuthGenerations[transaction.MargeAccountID] == transaction.PublicationGeneration
}

func (s *Server) supersedeSpotifyOAuthTransactions() {
	s.spotifySourceMu.Lock()
	defer s.spotifySourceMu.Unlock()

	s.spotifyOAuthMu.Lock()
	for accountID := range s.spotifyOAuthGenerations {
		s.spotifyOAuthGenerations[accountID]++
	}

	s.spotifyOAuthTransactions = make(map[string]spotifyOAuthTransaction)
	s.spotifyOAuthMu.Unlock()
}

func (s *Server) hasMargeAccount(accountID string) bool {
	if accountID == "" || accountID == "default" {
		return false
	}

	devices, err := s.ds.ListAllDevices()
	if err != nil {
		return false
	}

	for i := range devices {
		if devices[i].AccountID == accountID {
			return true
		}
	}

	return false
}
