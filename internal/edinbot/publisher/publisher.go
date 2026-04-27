// Package publisher is the diff-and-emit machine. It compares a Snapshot
// against persisted state for one binding and emits the minimal set of
// Discord actions: Post / Edit / Strike / Unstrike / Noop. Each successful
// Discord call is committed in its own transaction immediately
// (per-action atomicity per spec §3 'Publisher per-action atomicity').
package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/bindings"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

type Publisher struct {
	store store.Store
	dc    discordclient.Client
}

func New(s store.Store, dc discordclient.Client) *Publisher {
	return &Publisher{store: s, dc: dc}
}

// Apply rebuilds the channel from scratch every cycle: deletes every prior
// message the bot posted for this binding, then posts the current snapshot's
// items in price-ascending order.
//
// Why wipe-and-rebuild? Incremental reordering produced a "weird to watch"
// channel — messages shuffled in/out interleaved as divergence-detection
// chased rank slots. Full rebuild is louder per cycle but visually cleaner:
// old set vanishes, new set appears in the right order. Edit-in-place and
// noop optimizations are sacrificed; every cycle is uniform churn.
//
// Items that implement features.Sortable are sorted ASCENDING by SortKey, so
// chronologically the oldest message is the cheapest and the newest is the
// priciest. Items without Sortable fall back to alphabetical Identity sort.
//
// Side effects:
//   - "Posted X ago" timestamps are gone from the embed body — they would
//     always read "just now" after every cycle, so the annotation is noise.
//   - Strike/spoiler is no longer used: disappeared items are deleted, not
//     converted to spoilers. The strike code remains in the package for
//     potential future use but is unreachable from the current code path.
//
// TODO(scale): With N systems, every cycle = N deletes + N posts. Discord
// rate-limits at 5 ops/5s/channel; full rebuild of N=25 = ~25s per cycle
// (small fraction of the 15-min poll interval). N=100 = ~100s, still
// tolerable. If we approach that limit consider:
//   - Bulk delete (up to 100 messages in one API call, requires Manage
//     Messages and messages < 14 days old),
//   - Reverting to divergence-detect (more code, less visually clean),
//   - Forum-channel mode (separate manager, see TODO in feature.go).
func (p *Publisher) Apply(ctx context.Context, b bindings.Binding, s features.Snapshot) (Result, error) {
	res := Result{}

	disabled, err := p.store.IsBindingDisabled(ctx, b.ID)
	if err != nil {
		return res, fmt.Errorf("check disabled: %w", err)
	}
	if disabled {
		return res, nil
	}

	prev, err := p.store.GetPosted(ctx, b.ID)
	if err != nil {
		return res, fmt.Errorf("get posted: %w", err)
	}

	// Sort the desired order. Stable sort so identity is the secondary key
	// (ties on price → alphabetical, matching the per-system buyer sort).
	items := make([]features.Item, len(s.Items))
	copy(items, s.Items)
	sort.SliceStable(items, func(i, j int) bool {
		ki, oki := items[i].(features.Sortable)
		kj, okj := items[j].(features.Sortable)
		if oki && okj {
			if ki.SortKey() != kj.SortKey() {
				return ki.SortKey() < kj.SortKey()
			}
		}
		return items[i].Identity() < items[j].Identity()
	})

	// Wipe every existing message (live and any leftover spoiler) for this
	// binding. 404 on delete = already gone, treated as success. Any other
	// error short-circuits before we post fresh — better to leave stale
	// messages than risk duplicates.
	for _, prevRow := range prev {
		if prevRow.MessageID == "" {
			continue
		}
		err := p.dc.DeleteMessage(ctx, b.ChannelID, prevRow.MessageID)
		if err != nil && !errors.Is(err, discordclient.ErrMessageNotFound) {
			if errors.Is(err, discordclient.ErrChannelGone) {
				_ = p.store.DisableBinding(ctx, b.ID, s.GeneratedAt)
				res.Tally()
				return res, nil
			}
			return res, fmt.Errorf("wipe delete %s/%s: %w", prevRow.ChannelID, prevRow.MessageID, err)
		}
	}
	if _, err := p.store.DeletePostedForBinding(ctx, b.ID); err != nil {
		return res, fmt.Errorf("wipe posted_messages rows: %w", err)
	}

	// Post all current items fresh, in price-ascending order.
	for _, item := range items {
		embed := item.Render()
		msgID, err := p.dc.PostMessage(ctx, b.ChannelID, embed)
		if err != nil {
			if errors.Is(err, discordclient.ErrChannelGone) {
				_ = p.store.DisableBinding(ctx, b.ID, s.GeneratedAt)
				res.Tally()
				return res, nil
			}
			res.Items = append(res.Items, ItemResult{Identity: item.Identity(), Action: ActionPost, Err: err})
			continue
		}
		raw, _ := json.Marshal(embed)
		row := store.PostedMessage{
			BindingID:  b.ID,
			Identity:   item.Identity(),
			GuildID:    b.GuildID,
			ChannelID:  b.ChannelID,
			MessageID:  msgID,
			StateHash:  item.StateHash(),
			LastRender: raw,
			PostedAt:   s.GeneratedAt,
			LastSeenAt: s.GeneratedAt,
		}
		if err := p.store.UpsertPosted(ctx, row); err != nil {
			res.Items = append(res.Items, ItemResult{Identity: item.Identity(), Action: ActionPost,
				Err: fmt.Errorf("upsert after post: %w", err)})
			continue
		}
		res.Items = append(res.Items, ItemResult{Identity: item.Identity(), Action: ActionPost})
	}

	res.Tally()
	return res, nil
}

// repost deletes the existing Discord message (if any) and posts a fresh one.
// Used by Apply when an item's rank slot has changed and an edit-in-place
// would leave the channel out of order. PostedAt is set to `at` — important
// for the reappear-after-months case so the displayed "Posted X ago" reflects
// THIS appearance rather than a months-old original post.
//
// 404 / ErrMessageNotFound on the delete is treated as success (someone may
// have deleted the message manually). Any other delete error short-circuits
// before posting: we'd rather leave a stale message than risk duplicating.
func (p *Publisher) repost(ctx context.Context, b bindings.Binding, item features.Item, prev store.PostedMessage, at time.Time) ItemResult {
	identity := item.Identity()

	if prev.MessageID != "" {
		err := p.dc.DeleteMessage(ctx, b.ChannelID, prev.MessageID)
		if err != nil && !errors.Is(err, discordclient.ErrMessageNotFound) {
			return ItemResult{Identity: identity, Action: ActionPost, Err: fmt.Errorf("delete before repost: %w", err)}
		}
	}

	embed := AnnotateTimestamps(item.Render(), at, at)
	msgID, err := p.dc.PostMessage(ctx, b.ChannelID, embed)
	if err != nil {
		return ItemResult{Identity: identity, Action: ActionPost, Err: err}
	}
	raw, _ := json.Marshal(embed)
	row := store.PostedMessage{
		BindingID:  b.ID,
		Identity:   identity,
		GuildID:    b.GuildID,
		ChannelID:  b.ChannelID,
		MessageID:  msgID,
		StateHash:  item.StateHash(),
		LastRender: raw,
		PostedAt:   at,
		LastSeenAt: at,
	}
	if err := p.store.UpsertPosted(ctx, row); err != nil {
		return ItemResult{Identity: identity, Action: ActionPost, Err: fmt.Errorf("upsert after repost: %w", err)}
	}
	return ItemResult{Identity: identity, Action: ActionPost}
}

// applyOne handles one item: post (new), edit (changed), unstrike (returning),
// noop (unchanged). Each success commits its own UpsertPosted transaction.
func (p *Publisher) applyOne(ctx context.Context, b bindings.Binding, item features.Item, prev store.PostedMessage, at time.Time) ItemResult {
	identity := item.Identity()

	// New identity: post.
	if prev.MessageID == "" {
		embed := AnnotateTimestamps(item.Render(), at, at)
		msgID, err := p.dc.PostMessage(ctx, b.ChannelID, embed)
		if err != nil {
			return ItemResult{Identity: identity, Action: ActionPost, Err: err}
		}
		raw, _ := json.Marshal(embed)
		row := store.PostedMessage{
			BindingID:  b.ID,
			Identity:   identity,
			GuildID:    b.GuildID,
			ChannelID:  b.ChannelID,
			MessageID:  msgID,
			StateHash:  item.StateHash(),
			LastRender: raw,
			PostedAt:   at,
			LastSeenAt: at,
		}
		if err := p.store.UpsertPosted(ctx, row); err != nil {
			return ItemResult{Identity: identity, Action: ActionPost, Err: fmt.Errorf("upsert after post: %w", err)}
		}
		return ItemResult{Identity: identity, Action: ActionPost}
	}

	// Returning after a strike: unstrike (re-render fresh).
	if prev.StruckAt != nil {
		embed := AnnotateTimestamps(item.Render(), prev.PostedAt, at)
		if err := p.dc.EditMessage(ctx, b.ChannelID, prev.MessageID, embed); err != nil {
			return ItemResult{Identity: identity, Action: ActionUnstrike, Err: err}
		}
		raw, _ := json.Marshal(embed)
		row := prev
		row.StateHash = item.StateHash()
		row.LastRender = raw
		row.LastEditedAt = &at
		row.LastSeenAt = at
		row.StruckAt = nil
		row.UnstruckAt = &at
		if err := p.store.UpsertPosted(ctx, row); err != nil {
			return ItemResult{Identity: identity, Action: ActionUnstrike, Err: fmt.Errorf("upsert after unstrike: %w", err)}
		}
		return ItemResult{Identity: identity, Action: ActionUnstrike}
	}

	// Unchanged: noop. last_seen_at advances in the batch at end of Apply().
	if item.StateHash() == prev.StateHash {
		return ItemResult{Identity: identity, Action: ActionNoop}
	}

	// Changed: edit.
	embed := AnnotateTimestamps(item.Render(), prev.PostedAt, at)
	if err := p.dc.EditMessage(ctx, b.ChannelID, prev.MessageID, embed); err != nil {
		return ItemResult{Identity: identity, Action: ActionEdit, Err: err}
	}
	raw, _ := json.Marshal(embed)
	row := prev
	row.StateHash = item.StateHash()
	row.LastRender = raw
	row.LastEditedAt = &at
	row.LastSeenAt = at
	if err := p.store.UpsertPosted(ctx, row); err != nil {
		return ItemResult{Identity: identity, Action: ActionEdit, Err: fmt.Errorf("upsert after edit: %w", err)}
	}
	return ItemResult{Identity: identity, Action: ActionEdit}
}

// strike replaces the previously-posted embed with a one-line spoiler-wrapped
// "COMPLETED" message. Why not strikethrough? Strikethrough leaves a full-
// height greyed-out embed in the channel, which clutters the live feed.
// Spoilers collapse to a single black bar; the original render is preserved
// INSIDE the spoiler so a click-to-reveal still shows what the alert was.
//
// On un-strike, applyOne calls EditMessage with a fresh embed; that path uses
// ChannelMessageEditComplex to explicitly clear Content, so leftover spoiler
// text doesn't sit alongside the new embed.
func (p *Publisher) strike(ctx context.Context, b bindings.Binding, prev store.PostedMessage, at time.Time) ItemResult {
	var prior discordgo.MessageEmbed
	_ = json.Unmarshal(prev.LastRender, &prior) // best-effort; empty struct is fine for spoiler
	identity := prev.Identity
	systemName := identity
	if prior.Title != "" {
		systemName = prior.Title
	}
	spoiler := CompletedSpoiler(systemName, &prior, at.Unix())

	if err := p.dc.ReplaceWithText(ctx, b.ChannelID, prev.MessageID, spoiler); err != nil {
		return ItemResult{Identity: identity, Action: ActionStrike, Err: err}
	}

	// Persist the spoiler as the row's LastRender so we still have a record of
	// what's in Discord. Stored as a JSON envelope (not raw text) so the
	// existing []byte schema doesn't need changing.
	raw, _ := json.Marshal(map[string]string{"spoiler": spoiler})
	row := prev
	row.LastRender = raw
	row.LastEditedAt = &at
	row.StruckAt = &at
	if err := p.store.UpsertPosted(ctx, row); err != nil {
		return ItemResult{Identity: identity, Action: ActionStrike, Err: fmt.Errorf("upsert after strike: %w", err)}
	}
	return ItemResult{Identity: identity, Action: ActionStrike}
}

// ClearResult summarises a /admin/clear operation.
type ClearResult struct {
	BindingID      string `json:"binding_id"`
	DiscordDeleted int    `json:"discord_deleted"`     // 200 OK
	DiscordMissing int    `json:"discord_missing"`     // 404 — already gone, treated as success
	DiscordFailed  int    `json:"discord_failed"`      // any other error → row preserved
	RowsPurged     int    `json:"rows_purged"`         // posted_messages rows deleted from DB
	BindingEnabled bool   `json:"binding_enabled"`     // disabled_bindings entry was cleared
	Errors         []string `json:"errors,omitempty"`
}

// ClearHistory wipes Discord messages and DB rows for a binding so the next
// poll posts fresh. Iterates posted_messages rows, deletes each Discord
// message (404 treated as already-gone, 403 logged as a hard failure), then
// purges all rows for the binding ONLY IF every Discord delete succeeded
// (or was already missing) — partial deletes leave the bot's state matching
// reality. Always clears any disabled_bindings tombstone at the end so the
// scheduler will Poll again.
//
// Idempotent: calling twice on an empty binding is a no-op.
func (p *Publisher) ClearHistory(ctx context.Context, bindingID string) (ClearResult, error) {
	res := ClearResult{BindingID: bindingID}

	rows, err := p.store.GetPosted(ctx, bindingID)
	if err != nil {
		return res, fmt.Errorf("list posted: %w", err)
	}

	for _, row := range rows {
		err := p.dc.DeleteMessage(ctx, row.ChannelID, row.MessageID)
		switch {
		case err == nil:
			res.DiscordDeleted++
		case errors.Is(err, discordclient.ErrMessageNotFound):
			res.DiscordMissing++
		default:
			res.DiscordFailed++
			res.Errors = append(res.Errors,
				fmt.Sprintf("%s/%s: %v", row.ChannelID, row.MessageID, err))
		}
	}

	// Only purge rows if every Discord delete succeeded (or was already 404).
	// Otherwise the table would lie about state — there'd be live messages
	// in Discord with no row tracking them, and the next poll would create
	// duplicates.
	if res.DiscordFailed == 0 {
		n, err := p.store.DeletePostedForBinding(ctx, bindingID)
		if err != nil {
			return res, fmt.Errorf("delete posted_messages: %w", err)
		}
		res.RowsPurged = n
	}

	if err := p.store.EnableBinding(ctx, bindingID); err != nil {
		return res, fmt.Errorf("enable binding: %w", err)
	}
	res.BindingEnabled = true

	return res, nil
}
