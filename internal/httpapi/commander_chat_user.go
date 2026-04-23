package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/edin-space/edin-backend/internal/authz"
)

// CommanderChatUser identifies a commander who has completed the Frontier PKCE
// OAuth flow and holds a valid EDIN commander JWT. It is the only identity type
// the copilot chat WebSocket accepts.
//
// This is deliberately separate from KaineUser because copilot and the Kaine
// portal have different auth models:
//
//   - Kaine is Authentik-authenticated. Users carry groups like "kaine-approved"
//     and "kaine-chat", and access is gated by role checks (KaineUser.CanAccessChat
//     etc.).
//   - Copilot is Frontier-authenticated. Users carry an FID claim in a server-
//     issued RS256 JWT. Access is established when the JWT validates; there is
//     no secondary role check.
//
// The absence of Groups / role methods on this type is intentional. Adding them
// back here would silently re-couple the two auth models.
type CommanderChatUser struct {
	FID  string // Frontier ID, e.g. "F2504" — trust-root for all commander data access.
	Name string // Display name, e.g. "Pattern State".

	// Scopes granted to this chat session; populated from the JWT at
	// token-exchange time and threaded onto the tool-executor context so
	// per-tool authorisation flows through authz.ScopesFromContext.
	//
	// Task 2 populates this with a hardcoded default commander scope set
	// ({copilot_chat, galaxy_read, commander_data}) at the token-issue
	// site. Task 6 swaps the hardcode for scopes derived from the JWT's
	// "scopes" claim.
	Scopes []authz.Scope
}

// commanderChatNonceStore is an in-memory single-use nonce store with TTL for
// the copilot WebSocket first-message auth flow.
//
// Flow:
//  1. Frontend GETs /api/commander/auth/token with a valid commander session
//     cookie. The handler verifies the JWT and calls Issue, getting a nonce.
//  2. Frontend opens the copilot WebSocket and sends {"type":"auth","token":
//     "<nonce>"} as the first message.
//  3. The WS handler calls Consume. Any non-nil return is a valid commander
//     whose JWT was verified in step 1 — no further role check is performed.
//
// Single-use + TTL bounds the window for a leaked nonce. Nonces never leave the
// server process; the shorter the TTL the better. 10 seconds is typical.
//
// This is a parallel implementation to kaineNonceStore rather than a generic
// store because the two stores serve different user types and should be free to
// evolve independently (e.g. one might move to Redis before the other).
//
// In-memory is sufficient for a single-server deployment. Switch to Redis (or a
// shared store with the same interface) if scaling horizontally.
type commanderChatNonceStore struct {
	mu    sync.Mutex
	items map[string]commanderChatNonceEntry
}

type commanderChatNonceEntry struct {
	user      *CommanderChatUser
	expiresAt time.Time
}

func newCommanderChatNonceStore() *commanderChatNonceStore {
	s := &commanderChatNonceStore{items: make(map[string]commanderChatNonceEntry)}
	go s.cleanup()
	return s
}

// Issue creates a new single-use nonce for the commander, expiring in ttl.
// The returned nonce is a 32-char hex string (16 random bytes).
func (s *commanderChatNonceStore) Issue(user *CommanderChatUser, ttl time.Duration) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: time-based (should be practically unreachable on any
		// system with a working CSPRNG). Logged implicitly via short entropy.
		b = []byte(fmt.Sprintf("%016x", time.Now().UnixNano()))
	}
	nonce := hex.EncodeToString(b)

	s.mu.Lock()
	s.items[nonce] = commanderChatNonceEntry{user: user, expiresAt: time.Now().Add(ttl)}
	s.mu.Unlock()

	return nonce
}

// Consume retrieves and deletes the nonce. Returns nil if not found or expired.
// The entry is always deleted — even expired entries are removed so a leaked
// nonce cannot be replayed after expiry.
func (s *commanderChatNonceStore) Consume(nonce string) *CommanderChatUser {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.items[nonce]
	if !ok {
		return nil
	}

	delete(s.items, nonce)

	if time.Now().After(entry.expiresAt) {
		return nil
	}

	return entry.user
}

// cleanup removes expired entries every 30s. Runs for the lifetime of the
// process; there is no stop signal because the store lives for the lifetime of
// the Server.
func (s *commanderChatNonceStore) cleanup() {
	for {
		time.Sleep(30 * time.Second)
		now := time.Now()
		s.mu.Lock()
		for nonce, entry := range s.items {
			if now.After(entry.expiresAt) {
				delete(s.items, nonce)
			}
		}
		s.mu.Unlock()
	}
}
