package authentik

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// shadowUserPath is the Authentik users/ subtree where EDIN places auto-
// linked commander accounts. Shadow users are separated from human users
// (under "users") so the Authentik admin UI can list them at a glance and
// downstream policies can scope on path.
const shadowUserPath = "users/edin-commanders"

// shadowUserEmailDomain is the synthetic-email domain for shadow users. The
// .invalid TLD is reserved by RFC 2606 and guaranteed never to receive
// mail, so a typo or misconfiguration cannot leak to a real recipient.
//
// The local-part is the FID, e.g. "F2504@edin-shadow.invalid". This is
// stable per commander and unique by construction (FIDs are unique).
const shadowUserEmailDomain = "edin-shadow.invalid"

// CreateShadowUser creates (or finds, on duplicate-username) a shadow
// Authentik user for the given Frontier commander and returns its UUID.
//
// Shadow users have no login credential. They exist only to hold group
// memberships that drive scope resolution in resolveCommanderAccess. Do not
// reuse this helper to provision human Authentik users — those need
// passwords and a different path.
//
// Idempotency: if Authentik responds with a duplicate-username error
// (ErrDuplicateUsername), this function falls back to GetUserByUsername and
// returns the existing user's UUID. This handles the retry case where the
// commander row update failed AFTER the Authentik create succeeded — the
// next callback re-invokes CreateShadowUser, hits the duplicate, and
// recovers the same UUID so SetAuthentikLink can finish the job.
//
// CreateShadowUser is a free function (rather than a method on *Client) so
// httpapi.Server can wire it via a function-typed field for testability;
// see ensureCommanderLink.
func CreateShadowUser(ctx context.Context, c *Client, fid, cmdrName string) (uuid.UUID, error) {
	if c == nil {
		return uuid.Nil, errors.New("authentik: nil client")
	}
	if fid == "" {
		return uuid.Nil, errors.New("authentik: empty fid")
	}

	// Display name falls back to the FID when CAPI hasn't yielded a real
	// commander name yet (capi_pending=true on the first callback). The
	// name field can be updated on a later callback or admin action; what
	// matters for linking is the username (FID), which never changes.
	displayName := cmdrName
	if displayName == "" {
		displayName = fid
	}

	isActive := true
	req := CreateUserRequest{
		Username: fid,
		Name:     displayName,
		Email:    fid + "@" + shadowUserEmailDomain,
		Path:     shadowUserPath,
		IsActive: &isActive,
	}

	user, err := c.CreateUser(ctx, req)
	if err == nil {
		return user.UUID, nil
	}

	// Recover the existing UUID on duplicate username. Any other error is
	// surfaced to the caller, which deny-closes the auth flow.
	if !errors.Is(err, ErrDuplicateUsername) {
		return uuid.Nil, fmt.Errorf("authentik: create shadow user fid=%s: %w", fid, err)
	}

	existing, lookupErr := c.GetUserByUsername(ctx, fid)
	if lookupErr != nil {
		return uuid.Nil, fmt.Errorf(
			"authentik: shadow user fid=%s exists but lookup failed: %w", fid, lookupErr)
	}
	if existing == nil {
		// Defence in depth: GetUserByUsername should have returned
		// ErrUserNotFound rather than (nil, nil), but guard anyway.
		return uuid.Nil, fmt.Errorf(
			"authentik: shadow user fid=%s exists but lookup returned no row", fid)
	}
	return existing.UUID, nil
}
