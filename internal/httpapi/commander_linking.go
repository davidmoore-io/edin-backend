package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/google/uuid"
)

// errLinkPersistFailed is returned by ensureCommanderLink when Authentik has
// successfully created (or recovered) the shadow user but the database
// SetAuthentikLink write failed. The caller deny-closes the auth flow with
// reason="link_persist_failed". Subsequent callbacks are self-healing — the
// next CreateShadowUser hits the duplicate-username path and recovers the
// same UUID, and SetAuthentikLink runs again.
var errLinkPersistFailed = errors.New("commander_link: persist link to commander row failed")

// ensureCommanderLink guarantees that the commander row for fid is linked to
// an Authentik shadow user before the caller proceeds with access resolution.
// It is invoked from both the web and desktop Frontier callbacks, immediately
// after UpsertCommander has recorded (or refreshed) the row.
//
// Behaviour:
//   - Reads the row via GetCommanderAsAdmin (admin-tx — RLS would otherwise
//     hide the link column at this point in the request, before
//     app.current_fid is set).
//   - If the row is already linked (authentik_user_id IS NOT NULL), returns
//     (false, nil) — no Authentik call.
//   - Otherwise calls s.createShadowUser(ctx, fid, cmdrName), then
//     SetAuthentikLink. On Authentik error, returns (false, err) so the
//     caller can audit reason="authentik_unreachable". On link-persist
//     error, returns (true, errLinkPersistFailed) so the caller can audit
//     reason="link_persist_failed" — note createdShadow=true means
//     Authentik state is ahead of our DB.
//   - If the row is missing entirely, returns an error — the callback's
//     UpsertCommander runs immediately before this and is required to
//     succeed; a missing row at this point is a logic error, not user input.
//
// This function does NOT write the HTTP response — error handling and audit
// belong to the caller, which has the http.ResponseWriter and Request.
func (s *Server) ensureCommanderLink(ctx context.Context, fid, cmdrName string) (createdShadow bool, err error) {
	if s.commanderRepo == nil {
		return false, errors.New("commander_link: commander repository not configured")
	}

	row, err := s.commanderRepo.GetCommanderAsAdmin(ctx, fid)
	if err != nil {
		if errors.Is(err, store.ErrCommanderNotFound) {
			// Race or wiring bug: the callback's UpsertCommander runs
			// immediately before ensureCommanderLink, so the row must exist.
			// Treat this as an internal error — escalate, do NOT auto-create.
			return false, fmt.Errorf("commander_link: row missing after upsert fid=%s: %w", fid, err)
		}
		return false, fmt.Errorf("commander_link: read commander row fid=%s: %w", fid, err)
	}

	if row.AuthentikUserID != nil && *row.AuthentikUserID != uuid.Nil {
		// Already linked. No-op.
		return false, nil
	}

	if s.createShadowUser == nil {
		// Authentik is not configured for this deployment. Treat as an
		// auth failure — auto-link is now mandatory in the deployed
		// architecture, so a missing creator is a deployment defect, not
		// a graceful-degradation case.
		return false, errors.New("commander_link: shadow user creator not configured")
	}

	shadowID, err := s.createShadowUser(ctx, fid, cmdrName)
	if err != nil {
		return false, fmt.Errorf("commander_link: create shadow user fid=%s: %w", fid, err)
	}
	if shadowID == uuid.Nil {
		return false, fmt.Errorf("commander_link: shadow user creator returned nil UUID fid=%s", fid)
	}

	if err := s.commanderRepo.SetAuthentikLink(ctx, fid, &shadowID); err != nil {
		// Authentik state is ahead of our DB. The next callback will
		// duplicate-username on Authentik, recover the same UUID, and
		// retry SetAuthentikLink — self-healing.
		slog.Error("commander_link_persist_failed",
			"fid", fid,
			"shadow_uuid", shadowID.String(),
			"err", err,
		)
		return true, errLinkPersistFailed
	}

	slog.Info("commander_link_created",
		"fid", fid,
		"shadow_uuid", shadowID.String(),
	)
	return true, nil
}
