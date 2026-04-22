package httpapi

import (
	"testing"
	"time"
)

// TestKaineNonceStore_IssueAndConsume verifies a nonce can be issued and consumed.
func TestKaineNonceStore_IssueAndConsume(t *testing.T) {
	store := newKaineNonceStore()
	user := &KaineUser{Sub: "user123", Groups: []string{"kaine-chat"}}

	nonce := store.Issue(user, 10*time.Second)
	if nonce == "" {
		t.Fatal("expected non-empty nonce")
	}
	if len(nonce) != 32 {
		t.Errorf("nonce length = %d, want 32 (16 hex bytes)", len(nonce))
	}

	got := store.Consume(nonce)
	if got == nil {
		t.Fatal("expected user from Consume, got nil")
	}
	if got.Sub != user.Sub {
		t.Errorf("Consume returned Sub=%s, want %s", got.Sub, user.Sub)
	}
}

// TestKaineNonceStore_ConsumedNonce_SecondConsumeFails verifies nonces are single-use.
func TestKaineNonceStore_ConsumedNonce_SecondConsumeFails(t *testing.T) {
	store := newKaineNonceStore()
	user := &KaineUser{Sub: "user123", Groups: []string{"kaine-chat"}}

	nonce := store.Issue(user, 10*time.Second)

	// First consume succeeds
	first := store.Consume(nonce)
	if first == nil {
		t.Fatal("first Consume returned nil, expected user")
	}

	// Second consume must fail — nonce was already deleted
	second := store.Consume(nonce)
	if second != nil {
		t.Errorf("second Consume returned user, want nil (nonce is single-use)")
	}
}

// TestKaineNonceStore_ExpiredNonce_ReturnsNil verifies expired nonces are rejected.
func TestKaineNonceStore_ExpiredNonce_ReturnsNil(t *testing.T) {
	store := newKaineNonceStore()
	user := &KaineUser{Sub: "user123", Groups: []string{"kaine-chat"}}

	// Issue with a very short TTL
	nonce := store.Issue(user, 1*time.Millisecond)

	// Wait for it to expire
	time.Sleep(5 * time.Millisecond)

	got := store.Consume(nonce)
	if got != nil {
		t.Errorf("Consume returned user for expired nonce, want nil")
	}
}

// TestKaineNonceStore_UnknownNonce_ReturnsNil verifies unknown nonces are rejected.
func TestKaineNonceStore_UnknownNonce_ReturnsNil(t *testing.T) {
	store := newKaineNonceStore()

	got := store.Consume("totally-unknown-nonce-value")
	if got != nil {
		t.Errorf("Consume returned user for unknown nonce, want nil")
	}
}
