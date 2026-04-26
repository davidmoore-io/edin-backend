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

// strike wraps the previously-rendered embed in strikethrough markdown and
// edits it in place.
func (p *Publisher) strike(ctx context.Context, b bindings.Binding, prev store.PostedMessage, at time.Time) ItemResult {
	var embed discordgo.MessageEmbed
	if err := json.Unmarshal(prev.LastRender, &embed); err != nil {
		return ItemResult{Identity: prev.Identity, Action: ActionStrike, Err: fmt.Errorf("decode last_render: %w", err)}
	}
	struck := RenderStruckThrough(&embed, at)

	if err := p.dc.EditMessage(ctx, b.ChannelID, prev.MessageID, struck); err != nil {
		return ItemResult{Identity: prev.Identity, Action: ActionStrike, Err: err}
	}

	raw, _ := json.Marshal(struck)
	row := prev
	row.LastRender = raw
	row.LastEditedAt = &at
	row.StruckAt = &at
	if err := p.store.UpsertPosted(ctx, row); err != nil {
		return ItemResult{Identity: prev.Identity, Action: ActionStrike, Err: fmt.Errorf("upsert after strike: %w", err)}
	}
	return ItemResult{Identity: prev.Identity, Action: ActionStrike}
}
