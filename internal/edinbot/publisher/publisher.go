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
	"strconv"
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

// Apply diffs the snapshot against persisted state and emits per-item Discord
// actions. Each successful Discord call is committed in its own transaction
// immediately. Failures of one item do not abort the cycle. ErrChannelGone is
// fatal — it triggers DisableBinding and stops processing.
//
// Items that implement features.Sortable are sorted ASCENDING by SortKey before
// processing. The channel's chronological order then reflects price order:
// the oldest message is the cheapest, the newest is the priciest. To make
// that order true after each cycle, the publisher uses a divergence-detect
// algorithm: it walks the desired ordering against the existing chronological
// ordering, finds the first index where they differ, edits-in-place for items
// before that index, and DELETES + REPOSTS items from that index onward. A
// repost lands at the end of the channel; processing in desired-order means
// each repost lands in the correct relative slot.
//
// TODO(scale): Reorder-every-cycle is correct but unbounded in churn. With N
// systems, a single low-rank change can trigger up to N-1 delete+repost pairs.
// Discord rate-limits message ops at 5 per 5s per channel, so a full reshuffle
// of N=100 takes ~100 seconds. Tolerable today (typical N ≤ 25). If we ever
// approach those limits we should consider:
//   - Threshold-based reorder (only repost if rank-slot moved by ≥ K),
//   - Bucketing (post in price-tier groups, only reorder within tier),
//   - Manual-only reorder mode + edit-in-place between manual triggers.
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

	currentIdentities := map[string]bool{}
	for _, item := range items {
		currentIdentities[item.Identity()] = true
	}

	// Strike disappeared items first so the chronological list excludes them.
	for identity, prevRow := range prev {
		if currentIdentities[identity] {
			continue
		}
		if prevRow.StruckAt != nil {
			continue
		}
		ir := p.strike(ctx, b, prevRow, s.GeneratedAt)
		res.Items = append(res.Items, ir)
		if errors.Is(ir.Err, discordclient.ErrChannelGone) {
			_ = p.store.DisableBinding(ctx, b.ID, s.GeneratedAt)
			res.Tally()
			return res, nil
		}
	}

	// Build the chronological order of currently-live messages (in-snapshot,
	// not struck). This is what's actually visible in Discord right now.
	type chronoEntry struct {
		identity string
		row      store.PostedMessage
	}
	chronological := make([]chronoEntry, 0, len(items))
	for _, item := range items {
		row, ok := prev[item.Identity()]
		if !ok || row.StruckAt != nil || row.MessageID == "" {
			continue
		}
		chronological = append(chronological, chronoEntry{item.Identity(), row})
	}
	// Sort by MessageID parsed as int64. Discord IDs are snowflakes (64-bit
	// timestamp-based, monotonically increasing) — much more reliable than
	// PostedAt for chronological order, because all messages posted in one
	// Apply() call share the same Snapshot.GeneratedAt timestamp and would
	// otherwise compare equal. Parse failures fall back to string compare,
	// which is fine for fakes/tests where message IDs are "fake-msg-1" etc.
	sort.SliceStable(chronological, func(i, j int) bool {
		ai, aerr := strconv.ParseInt(chronological[i].row.MessageID, 10, 64)
		bi, berr := strconv.ParseInt(chronological[j].row.MessageID, 10, 64)
		if aerr == nil && berr == nil {
			return ai < bi
		}
		return chronological[i].row.MessageID < chronological[j].row.MessageID
	})

	// Find the first divergence between desired (items) and current
	// (chronological). Everything before this index is correctly positioned
	// → edit-in-place. Everything from this index onward needs to be
	// (re)posted in the desired order so chronological order matches.
	divergeAt := len(items)
	for i := 0; i < len(items) && i < len(chronological); i++ {
		if items[i].Identity() != chronological[i].identity {
			divergeAt = i
			break
		}
	}
	if len(chronological) < len(items) && divergeAt > len(chronological) {
		divergeAt = len(chronological)
	}

	// [0, divergeAt) — stable position. Apply edits / unstrike fresh / noop.
	for i := 0; i < divergeAt; i++ {
		item := items[i]
		ir := p.applyOne(ctx, b, item, prev[item.Identity()], s.GeneratedAt)
		res.Items = append(res.Items, ir)
		if errors.Is(ir.Err, discordclient.ErrChannelGone) {
			_ = p.store.DisableBinding(ctx, b.ID, s.GeneratedAt)
			res.Tally()
			return res, nil
		}
	}

	// [divergeAt, end) — repost in desired order. Each iteration: if a prior
	// row exists for this identity, delete its Discord message; then post
	// fresh and upsert. This is what makes the channel's chronological order
	// match the desired (price) order.
	for i := divergeAt; i < len(items); i++ {
		ir := p.repost(ctx, b, items[i], prev[items[i].Identity()], s.GeneratedAt)
		res.Items = append(res.Items, ir)
		if errors.Is(ir.Err, discordclient.ErrChannelGone) {
			_ = p.store.DisableBinding(ctx, b.ID, s.GeneratedAt)
			res.Tally()
			return res, nil
		}
	}

	seen := make([]string, 0, len(currentIdentities))
	for id := range currentIdentities {
		seen = append(seen, id)
	}
	if err := p.store.UpdateLastSeen(ctx, b.ID, seen, s.GeneratedAt); err != nil {
		return res, fmt.Errorf("update last_seen: %w", err)
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
