package watcher_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features/watcher"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

// fakeStore is a hand-written in-memory implementation of watcher.Store.
// It mirrors PostgresStore's contract closely so the handlers can't
// distinguish; in particular AddWatch returns ErrAlreadyWatched on a PK
// collision and CountWatchesInChannel scopes by channel.
type fakeStore struct {
	mu    sync.Mutex
	rows  map[string]store.WatchedSystem // key = channelID + "|" + slug
	addEr error                          // injected error for AddWatch
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]store.WatchedSystem{}} }

func key(channelID, slug string) string { return channelID + "|" + slug }

func (s *fakeStore) AddWatch(ctx context.Context, w store.WatchedSystem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addEr != nil {
		return s.addEr
	}
	k := key(w.ChannelID, w.SystemSlug)
	if _, exists := s.rows[k]; exists {
		return store.ErrAlreadyWatched
	}
	s.rows[k] = w
	return nil
}

func (s *fakeStore) RemoveWatch(ctx context.Context, channelID, slug string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(channelID, slug)
	if _, exists := s.rows[k]; !exists {
		return false, nil
	}
	delete(s.rows, k)
	return true, nil
}

func (s *fakeStore) GetWatch(ctx context.Context, channelID, slug string) (*store.WatchedSystem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.rows[key(channelID, slug)]; ok {
		w := w
		return &w, nil
	}
	return nil, nil
}

func (s *fakeStore) ListAllWatches(ctx context.Context) ([]store.WatchedSystem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.WatchedSystem, 0, len(s.rows))
	for _, w := range s.rows {
		out = append(out, w)
	}
	return out, nil
}

func (s *fakeStore) CountWatchesInChannel(ctx context.Context, channelID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, w := range s.rows {
		if w.ChannelID == channelID {
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) UpdateWatchState(ctx context.Context, channelID, slug, hash string, render []byte, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(channelID, slug)
	w, ok := s.rows[k]
	if !ok {
		return errors.New("not found")
	}
	w.LastStateHash = hash
	w.LastRender = render
	w.LastUpdatedAt = updatedAt
	s.rows[k] = w
	return nil
}

// fakeSnapshotter implements watcher.Snapshotter. Returns a fixture or
// an injected error.
type fakeSnapshotter struct {
	resp *controlclient.SystemWatchSnapshot
	err  error
}

func (f *fakeSnapshotter) GetSystemWatchSnapshot(ctx context.Context, slug string) (*controlclient.SystemWatchSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// fakeDiscord records every call. Implements watcher.Discord.
type fakeDiscord struct {
	mu        sync.Mutex
	posts     []string // channelID|messageID after each PostMessage
	edits     int
	deletes   []string // channelID|messageID
	postErr   error
	editErr   error
	deleteErr error
	nextID    int
}

func (d *fakeDiscord) PostMessage(ctx context.Context, channelID string, embed *discordgo.MessageEmbed) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.postErr != nil {
		return "", d.postErr
	}
	d.nextID++
	id := "msg-" + intToStr(d.nextID)
	d.posts = append(d.posts, channelID+"|"+id)
	return id, nil
}
func (d *fakeDiscord) EditMessage(ctx context.Context, channelID, messageID string, embed *discordgo.MessageEmbed) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.editErr != nil {
		return d.editErr
	}
	d.edits++
	return nil
}
func (d *fakeDiscord) DeleteMessage(ctx context.Context, channelID, messageID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.deleteErr != nil {
		return d.deleteErr
	}
	d.deletes = append(d.deletes, channelID+"|"+messageID)
	return nil
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// recordingResp records every InteractionRespond call so tests can
// observe the ephemeral text the handler produced.
type recordingResp struct {
	mu      sync.Mutex
	replies []string
}

func (r *recordingResp) InteractionRespond(ic *discordgo.Interaction, resp *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if resp.Data != nil {
		r.replies = append(r.replies, resp.Data.Content)
	}
	return nil
}
func (r *recordingResp) InteractionResponseEdit(ic *discordgo.Interaction, edit *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	return &discordgo.Message{}, nil
}
func (r *recordingResp) FollowupMessageCreate(ic *discordgo.Interaction, _ bool, params *discordgo.WebhookParams, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	return &discordgo.Message{}, nil
}
func (r *recordingResp) Last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.replies) == 0 {
		return ""
	}
	return r.replies[len(r.replies)-1]
}

// mkInteraction builds an InteractionCreate carrying the slash command
// option "system" with the supplied input.
func mkInteraction(channelID, userID, system string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:      discordgo.InteractionApplicationCommand,
			ChannelID: channelID,
			Member: &discordgo.Member{
				User: &discordgo.User{ID: userID},
			},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: "watch",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{Name: "system", Type: discordgo.ApplicationCommandOptionString, Value: system},
				},
			},
		},
	}
}

// loadHIPSnapshot reads the same fixture render_test uses.
func mkSnapshot() *controlclient.SystemWatchSnapshot {
	return &controlclient.SystemWatchSnapshot{
		Slug:             "HIP61332",
		Name:             "HIP 61332",
		ControllingPower: "Felicia Winters",
		PowerplayState:   "Stronghold",
		Powers:           []string{"Felicia Winters"},
	}
}

func mkDeps(st *fakeStore, snap *fakeSnapshotter, dc *fakeDiscord) watcher.HandlerDeps {
	return watcher.HandlerDeps{
		Store:   st,
		Snap:    snap,
		Discord: dc,
		Cfg:     watcher.Config{}, // defaults: 50 / 120s / 1s
		GuildID: "kaine-guild",
		NowFunc: func() time.Time { return time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC) },
		LogFunc: func(format string, args ...any) {}, // silent
	}
}

// ---- /watch happy path + branches ----

func TestWatch_HappyPath(t *testing.T) {
	st, snap, dc := newFakeStore(), &fakeSnapshotter{resp: mkSnapshot()}, &fakeDiscord{}
	deps := mkDeps(st, snap, dc)

	resp := &recordingResp{}
	require.NoError(t, watcher.Watch(deps)(context.Background(), resp,
		mkInteraction("watch-channel", "user-1", "HIP 61332")))

	require.Len(t, dc.posts, 1, "exactly one Discord post on happy path")
	require.Contains(t, resp.Last(), "Now watching **HIP 61332**")
	require.Contains(t, resp.Last(), "https://discord.com/channels/")

	// Row persisted with the correct creator and freshness fields.
	got, err := st.GetWatch(context.Background(), "watch-channel", "HIP61332")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "user-1", got.CreatedBy)
	require.Equal(t, "kaine-guild", got.GuildID)
	require.NotEmpty(t, got.LastStateHash, "state hash must be persisted on the initial post")
}

func TestWatch_EmptyInput(t *testing.T) {
	st, snap, dc := newFakeStore(), &fakeSnapshotter{resp: mkSnapshot()}, &fakeDiscord{}
	resp := &recordingResp{}
	require.NoError(t, watcher.Watch(mkDeps(st, snap, dc))(context.Background(), resp,
		mkInteraction("watch-channel", "u1", "  ")))
	require.Contains(t, resp.Last(), "Please provide a system name")
	require.Empty(t, dc.posts)
}

func TestWatch_SystemNotFoundIsPolite(t *testing.T) {
	st := newFakeStore()
	snap := &fakeSnapshotter{err: controlclient.ErrSystemNotFound}
	dc := &fakeDiscord{}
	resp := &recordingResp{}
	require.NoError(t, watcher.Watch(mkDeps(st, snap, dc))(context.Background(), resp,
		mkInteraction("watch-channel", "u1", "Imaginary System")))
	require.Contains(t, resp.Last(), "I can't find a system named **Imaginary System**")
	require.Empty(t, dc.posts, "no Discord post when system unknown")
	require.Empty(t, st.rows, "no row persisted when system unknown")
}

func TestWatch_AlreadyWatchedShowsMessageLink(t *testing.T) {
	st := newFakeStore()
	// Pre-populate an existing watch — that's what triggers the polite
	// rejection branch.
	require.NoError(t, st.AddWatch(context.Background(), store.WatchedSystem{
		GuildID: "kaine-guild", ChannelID: "watch-channel",
		SystemSlug: "HIP61332", SystemName: "HIP 61332",
		MessageID: "existing-msg", CreatedBy: "u-prior",
		WatchedAt: time.Now(), LastUpdatedAt: time.Now(),
		LastStateHash: "h", LastRender: []byte(`{}`),
	}))

	snap := &fakeSnapshotter{resp: mkSnapshot()}
	dc := &fakeDiscord{}
	resp := &recordingResp{}
	require.NoError(t, watcher.Watch(mkDeps(st, snap, dc))(context.Background(), resp,
		mkInteraction("watch-channel", "u-new", "HIP 61332")))

	require.Contains(t, resp.Last(), "is already being watched")
	require.Contains(t, resp.Last(), "discord.com/channels/kaine-guild/watch-channel/existing-msg")
	require.Empty(t, dc.posts, "no second post when already watched")
}

func TestWatch_ChannelCapReached(t *testing.T) {
	st := newFakeStore()
	// Cfg.MaxWatchesPerChannel defaults to 50; pre-seed 50 rows.
	for i := 0; i < 50; i++ {
		slug := "Filler" + intToStr(i)
		require.NoError(t, st.AddWatch(context.Background(), store.WatchedSystem{
			GuildID: "kaine-guild", ChannelID: "watch-channel",
			SystemSlug: slug, SystemName: slug,
			MessageID: "m-" + slug, CreatedBy: "u",
			WatchedAt: time.Now(), LastUpdatedAt: time.Now(),
			LastStateHash: "h", LastRender: []byte(`{}`),
		}))
	}

	snap := &fakeSnapshotter{resp: mkSnapshot()}
	dc := &fakeDiscord{}
	resp := &recordingResp{}
	require.NoError(t, watcher.Watch(mkDeps(st, snap, dc))(context.Background(), resp,
		mkInteraction("watch-channel", "u1", "HIP 61332")))

	require.Contains(t, resp.Last(), "maximum 50 watches")
	require.Empty(t, dc.posts)
}

func TestWatch_DiscordPostFails_RowNotPersisted(t *testing.T) {
	st, snap, dc := newFakeStore(), &fakeSnapshotter{resp: mkSnapshot()}, &fakeDiscord{
		postErr: errors.New("discord 500"),
	}
	resp := &recordingResp{}
	require.NoError(t, watcher.Watch(mkDeps(st, snap, dc))(context.Background(), resp,
		mkInteraction("watch-channel", "u1", "HIP 61332")))

	require.Contains(t, resp.Last(), "I couldn't post the watch message")
	require.Empty(t, st.rows, "no DB row when the Discord post failed")
}

func TestWatch_RaceConditionRollsBackPost(t *testing.T) {
	// Two parallel /watches both pass the GetWatch check; the loser's
	// AddWatch hits the unique-violation. We must roll back the loser's
	// just-posted Discord message so we don't leak orphans.
	st := newFakeStore()
	st.addEr = store.ErrAlreadyWatched // simulate the race on AddWatch
	snap := &fakeSnapshotter{resp: mkSnapshot()}
	dc := &fakeDiscord{}
	resp := &recordingResp{}
	require.NoError(t, watcher.Watch(mkDeps(st, snap, dc))(context.Background(), resp,
		mkInteraction("watch-channel", "u1", "HIP 61332")))

	require.Contains(t, resp.Last(), "is already being watched")
	require.Len(t, dc.posts, 1, "post happened before the AddWatch race lost")
	require.Len(t, dc.deletes, 1, "loser must roll back its post")
}

// ---- /unwatch ----

func TestUnwatch_HappyPath(t *testing.T) {
	st := newFakeStore()
	require.NoError(t, st.AddWatch(context.Background(), store.WatchedSystem{
		GuildID: "kaine-guild", ChannelID: "watch-channel",
		SystemSlug: "HIP61332", SystemName: "HIP 61332",
		MessageID: "m-1", CreatedBy: "u-1",
		WatchedAt: time.Now(), LastUpdatedAt: time.Now(),
		LastStateHash: "h", LastRender: []byte(`{}`),
	}))

	dc := &fakeDiscord{}
	resp := &recordingResp{}
	deps := mkDeps(st, &fakeSnapshotter{resp: mkSnapshot()}, dc)
	require.NoError(t, watcher.Unwatch(deps)(context.Background(), resp,
		mkInteraction("watch-channel", "u-1", "HIP 61332")))

	require.Contains(t, resp.Last(), "Stopped watching **HIP 61332**")
	require.Len(t, dc.deletes, 1)
	got, _ := st.GetWatch(context.Background(), "watch-channel", "HIP61332")
	require.Nil(t, got, "row must be removed")
}

func TestUnwatch_NotCurrentlyWatched(t *testing.T) {
	st, dc := newFakeStore(), &fakeDiscord{}
	resp := &recordingResp{}
	deps := mkDeps(st, &fakeSnapshotter{resp: mkSnapshot()}, dc)
	require.NoError(t, watcher.Unwatch(deps)(context.Background(), resp,
		mkInteraction("watch-channel", "u-1", "Definitely Not Here")))

	require.Contains(t, resp.Last(), "is not currently being watched")
	require.Empty(t, dc.deletes, "no Discord delete when row didn't exist")
}

func TestUnwatch_DiscordMessageAlreadyGone(t *testing.T) {
	// Operator manually deleted the Discord message but the DB row is
	// still there. /unwatch must succeed by treating the 404 as "ok,
	// already gone" and tidying the row.
	st := newFakeStore()
	require.NoError(t, st.AddWatch(context.Background(), store.WatchedSystem{
		GuildID: "kaine-guild", ChannelID: "watch-channel",
		SystemSlug: "HIP61332", SystemName: "HIP 61332",
		MessageID: "m-1", CreatedBy: "u-1",
		WatchedAt: time.Now(), LastUpdatedAt: time.Now(),
		LastStateHash: "h", LastRender: []byte(`{}`),
	}))

	dc := &fakeDiscord{deleteErr: discordclient.ErrMessageNotFound}
	resp := &recordingResp{}
	deps := mkDeps(st, &fakeSnapshotter{resp: mkSnapshot()}, dc)
	require.NoError(t, watcher.Unwatch(deps)(context.Background(), resp,
		mkInteraction("watch-channel", "u-1", "HIP 61332")))

	require.Contains(t, resp.Last(), "Stopped watching")
	got, _ := st.GetWatch(context.Background(), "watch-channel", "HIP61332")
	require.Nil(t, got, "row removed even when the Discord message was already gone")
}
