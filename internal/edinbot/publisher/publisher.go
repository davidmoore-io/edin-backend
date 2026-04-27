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

	currentIdentities := map[string]bool{}
	for _, item := range s.Items {
		currentIdentities[item.Identity()] = true
		ir := p.applyOne(ctx, b, item, prev[item.Identity()], s.GeneratedAt)
		res.Items = append(res.Items, ir)
		if errors.Is(ir.Err, discordclient.ErrChannelGone) {
			_ = p.store.DisableBinding(ctx, b.ID, s.GeneratedAt)
			res.Tally()
			return res, nil
		}
	}

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
