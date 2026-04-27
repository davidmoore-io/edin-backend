package publisher_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/edin-space/edin-backend/internal/edinbot/bindings"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/edin-space/edin-backend/internal/edinbot/publisher"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
	"github.com/stretchr/testify/require"
)

// stubItem is a test Item.
type stubItem struct {
	id    string
	hash  string
	embed *discordgo.MessageEmbed
}

func (s *stubItem) Identity() string                { return s.id }
func (s *stubItem) StateHash() string               { return s.hash }
func (s *stubItem) Render() *discordgo.MessageEmbed { return s.embed }

func mkItem(id, title string) *stubItem {
	h := sha256.Sum256([]byte(id + title))
	return &stubItem{
		id:    id,
		hash:  hex.EncodeToString(h[:]),
		embed: &discordgo.MessageEmbed{Title: title, Description: "body"},
	}
}

// memStore is a hand-written in-memory Store for publisher tests. Behaves
// identically to PostgresStore on the methods publisher uses.
type memStore struct {
	mu       sync.Mutex
	posted   map[string]map[string]store.PostedMessage // bindingID → identity → row
	cycles   []store.PollCycle
	disabled map[string]time.Time
}

func newMemStore() *memStore {
	return &memStore{
		posted:   map[string]map[string]store.PostedMessage{},
		disabled: map[string]time.Time{},
	}
}

func (m *memStore) GetPosted(ctx context.Context, bindingID string) (map[string]store.PostedMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]store.PostedMessage{}
	for k, v := range m.posted[bindingID] {
		out[k] = v
	}
	return out, nil
}

func (m *memStore) UpsertPosted(ctx context.Context, p store.PostedMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.posted[p.BindingID]; !ok {
		m.posted[p.BindingID] = map[string]store.PostedMessage{}
	}
	m.posted[p.BindingID][p.Identity] = p
	return nil
}

func (m *memStore) MarkStruck(ctx context.Context, bid, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.posted[bid][id]
	row.StruckAt = &at
	m.posted[bid][id] = row
	return nil
}

func (m *memStore) MarkUnstruck(ctx context.Context, bid, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.posted[bid][id]
	row.StruckAt = nil
	row.UnstruckAt = &at
	m.posted[bid][id] = row
	return nil
}

func (m *memStore) UpdateLastSeen(ctx context.Context, bid string, ids []string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if row, ok := m.posted[bid][id]; ok {
			row.LastSeenAt = at
			m.posted[bid][id] = row
		}
	}
	return nil
}

func (m *memStore) DisableBinding(ctx context.Context, bid string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disabled[bid] = at
	for id, row := range m.posted[bid] {
		row.DisabledAt = &at
		m.posted[bid][id] = row
	}
	return nil
}

func (m *memStore) IsBindingDisabled(ctx context.Context, bid string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.disabled[bid]
	return ok, nil
}

func (m *memStore) DeletePostedForBinding(ctx context.Context, bid string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.posted[bid])
	delete(m.posted, bid)
	return n, nil
}

func (m *memStore) EnableBinding(ctx context.Context, bid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.disabled, bid)
	return nil
}

func (m *memStore) RecordPollCycle(ctx context.Context, c store.PollCycle) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cycles = append(m.cycles, c)
	return nil
}

func (m *memStore) RecordDiagnoseReport(ctx context.Context, r store.DiagnoseReport) error {
	return nil
}

func (m *memStore) LatestSuccessAt(ctx context.Context, bid string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest time.Time
	for _, c := range m.cycles {
		if c.BindingID == bid && (c.Status == "success" || c.Status == "event") && c.TickedAt.After(latest) {
			latest = c.TickedAt
		}
	}
	return latest, nil
}

func bnd() bindings.Binding {
	return bindings.Binding{
		ID:        "test-binding",
		GuildID:   "g1",
		ChannelID: "c1",
	}
}

func snap(items ...features.Item) features.Snapshot {
	return features.Snapshot{
		Items:       items,
		Healthy:     true,
		GeneratedAt: time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC),
	}
}

// ---- the actual tests ----

func TestPublisher_FirstSnapshot_PostsEverything(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	res, err := p.Apply(context.Background(), bnd(), snap(mkItem("system:Sol", "Sol")))
	require.NoError(t, err)
	require.Equal(t, 1, res.Posted)
	require.Len(t, dc.PostCalls(), 1)
	require.Equal(t, "c1", dc.PostCalls()[0].ChannelID)

	posted, _ := st.GetPosted(context.Background(), "test-binding")
	require.Contains(t, posted, "system:Sol")
	require.NotEmpty(t, posted["system:Sol"].MessageID)
}

// New model: every cycle wipes and rebuilds. Same input two cycles in a row =
// double the deletes + double the posts (no edit/noop optimization). The
// channel ends up identical but Discord traffic is uniform per cycle.
func TestPublisher_RebuildsEveryCycle(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	item := mkItem("system:Sol", "Sol")
	_, err := p.Apply(context.Background(), bnd(), snap(item))
	require.NoError(t, err)
	require.Len(t, dc.PostCalls(), 1)
	require.Empty(t, dc.DeleteCalls(), "first cycle has nothing to delete")
	dc.Reset()

	_, err = p.Apply(context.Background(), bnd(), snap(item))
	require.NoError(t, err)
	require.Len(t, dc.DeleteCalls(), 1, "second cycle wipes prior message")
	require.Len(t, dc.PostCalls(), 1, "and posts the same item fresh")
}

// New model: an item that disappears from the snapshot is DELETED, not
// converted to a spoiler. The channel reflects only current state.
func TestPublisher_DisappearedItem_Deleted(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	a := mkItem("system:A", "A")
	b := mkItem("system:B", "B")
	_, err := p.Apply(context.Background(), bnd(), snap(a, b))
	require.NoError(t, err)
	dc.Reset()

	// B disappears; only A remains.
	res, err := p.Apply(context.Background(), bnd(), snap(a))
	require.NoError(t, err)
	require.Empty(t, dc.ReplaceTextCalls(), "no spoiler conversion in the new model")
	require.Len(t, dc.DeleteCalls(), 2, "both prior messages deleted")
	require.Len(t, dc.PostCalls(), 1, "only A reposts")
	require.Equal(t, 1, res.Posted)

	posted, _ := st.GetPosted(context.Background(), "test-binding")
	_, hasB := posted["system:B"]
	require.False(t, hasB, "disappeared item is gone from the table too")
}

func TestPublisher_ReappearedItem_FreshPostedAt(t *testing.T) {
	// A system that disappeared then reappeared months later must show the
	// fresh PostedAt — wipe-and-rebuild gives this for free since every cycle
	// reposts and writes PostedAt = snapshot.GeneratedAt.
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	item := mkItem("system:A", "A")
	_, _ = p.Apply(context.Background(), bnd(), snap(item))
	_, _ = p.Apply(context.Background(), bnd(), snap()) // disappears
	dc.Reset()

	t1 := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC) // months later
	freshSnap := features.Snapshot{Items: []features.Item{item}, Healthy: true, GeneratedAt: t1}
	res, err := p.Apply(context.Background(), bnd(), freshSnap)
	require.NoError(t, err)
	require.Equal(t, 1, res.Posted)

	posted, _ := st.GetPosted(context.Background(), "test-binding")
	require.WithinDuration(t, t1, posted["system:A"].PostedAt, time.Second,
		"posted_at reflects this cycle, not the original months-old post")
}

func TestPublisher_PartialFailure_DoesNotAbortOthers(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	a := mkItem("system:A", "A")
	b := mkItem("system:B", "B")
	_, _ = p.Apply(context.Background(), bnd(), snap(a, b))
	dc.Reset()

	// PostErr fires for every post in this cycle; both items fail to post
	// after the prior wipe, but neither aborts the loop.
	dc.PostErr = errors.New("temporary 500")
	res, err := p.Apply(context.Background(), bnd(), snap(a, b))
	require.NoError(t, err, "partial failure does NOT abort the cycle")
	require.Equal(t, 2, res.Failed, "both posts failed")

	identities := []string{}
	for _, it := range res.Items {
		identities = append(identities, it.Identity)
	}
	sort.Strings(identities)
	require.Equal(t, []string{"system:A", "system:B"}, identities)
}

func TestPublisher_ChannelGoneError_DisablesBinding(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	dc.PostErr = discordclient.ErrChannelGone
	p := publisher.New(st, dc)

	_, err := p.Apply(context.Background(), bnd(), snap(mkItem("system:A", "A")))
	require.NoError(t, err)

	disabled, _ := st.IsBindingDisabled(context.Background(), "test-binding")
	require.True(t, disabled, "ErrChannelGone must trigger DisableBinding")
}

func TestPublisher_DisabledBinding_SkipsAllDiscordCalls(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	require.NoError(t, st.DisableBinding(context.Background(), "test-binding", time.Now()))
	p := publisher.New(st, dc)

	res, err := p.Apply(context.Background(), bnd(), snap(mkItem("system:A", "A")))
	require.NoError(t, err)
	require.Equal(t, 0, res.Posted)
	require.Empty(t, dc.PostCalls(), "no Discord calls allowed for a disabled binding")
}


func TestPublisher_PersistsLastRender(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	_, _ = p.Apply(context.Background(), bnd(), snap(mkItem("system:A", "A")))

	posted, _ := st.GetPosted(context.Background(), "test-binding")
	require.NotEmpty(t, posted["system:A"].LastRender)

	var roundtrip discordgo.MessageEmbed
	require.NoError(t, json.Unmarshal(posted["system:A"].LastRender, &roundtrip))
	require.Equal(t, "A", roundtrip.Title)
}

// Restart safety in the new model: pre-existing posted_messages row gets
// deleted (its prior Discord message was wiped or will be after restart)
// and the system reposts fresh. Continuity of message-ID is gone — the
// embed content is the same, but it's a new message in the channel.
func TestPublisher_RestartScenario_DeletesPriorAndReposts(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	at := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	require.NoError(t, st.UpsertPosted(context.Background(), store.PostedMessage{
		BindingID:  "test-binding",
		Identity:   "system:Sol",
		GuildID:    "g1",
		ChannelID:  "c1",
		MessageID:  "pre-existing-msg-42",
		StateHash:  "old-hash",
		LastRender: []byte(`{"title":"Sol-old"}`),
		PostedAt:   at,
		LastSeenAt: at,
	}))

	res, err := p.Apply(context.Background(), bnd(), snap(mkItem("system:Sol", "Sol-new")))
	require.NoError(t, err)
	require.Equal(t, 1, res.Posted)
	require.Len(t, dc.DeleteCalls(), 1, "prior row's message must be deleted before repost")
	require.Equal(t, "pre-existing-msg-42", dc.DeleteCalls()[0].MessageID)
	require.Len(t, dc.PostCalls(), 1, "fresh post after the wipe")
}

func TestClearHistory_DeletesMessagesAndRows(t *testing.T) {
	at := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	st := &memStore{posted: map[string]map[string]store.PostedMessage{}, disabled: map[string]time.Time{}}
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	require.NoError(t, st.UpsertPosted(context.Background(), store.PostedMessage{
		BindingID: "b", Identity: "sol", ChannelID: "c1", MessageID: "m1",
		StateHash: "h1", PostedAt: at, LastSeenAt: at,
	}))
	require.NoError(t, st.UpsertPosted(context.Background(), store.PostedMessage{
		BindingID: "b", Identity: "wolf", ChannelID: "c1", MessageID: "m2",
		StateHash: "h2", PostedAt: at, LastSeenAt: at,
	}))
	require.NoError(t, st.DisableBinding(context.Background(), "b", at))

	res, err := p.ClearHistory(context.Background(), "b")
	require.NoError(t, err)
	require.Equal(t, 2, res.DiscordDeleted)
	require.Equal(t, 0, res.DiscordFailed)
	require.Equal(t, 2, res.RowsPurged)
	require.True(t, res.BindingEnabled)

	require.Len(t, dc.DeleteCalls(), 2)

	rows, _ := st.GetPosted(context.Background(), "b")
	require.Empty(t, rows, "rows purged after successful deletes")

	disabled, _ := st.IsBindingDisabled(context.Background(), "b")
	require.False(t, disabled, "disabled tombstone cleared")
}

func TestClearHistory_404TreatedAsAlreadyGone(t *testing.T) {
	at := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	st := &memStore{posted: map[string]map[string]store.PostedMessage{}, disabled: map[string]time.Time{}}
	dc := discordclient.NewFakeDiscordClient()
	dc.DeleteErr = discordclient.ErrMessageNotFound
	p := publisher.New(st, dc)

	require.NoError(t, st.UpsertPosted(context.Background(), store.PostedMessage{
		BindingID: "b", Identity: "sol", ChannelID: "c1", MessageID: "m1",
		StateHash: "h1", PostedAt: at, LastSeenAt: at,
	}))

	res, err := p.ClearHistory(context.Background(), "b")
	require.NoError(t, err)
	require.Equal(t, 0, res.DiscordDeleted)
	require.Equal(t, 1, res.DiscordMissing)
	require.Equal(t, 0, res.DiscordFailed)
	require.Equal(t, 1, res.RowsPurged, "missing-from-discord still purges row")
}

func TestClearHistory_HardFailurePreservesRows(t *testing.T) {
	at := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	st := &memStore{posted: map[string]map[string]store.PostedMessage{}, disabled: map[string]time.Time{}}
	dc := discordclient.NewFakeDiscordClient()
	dc.DeleteErr = errors.New("network is on fire")
	p := publisher.New(st, dc)

	require.NoError(t, st.UpsertPosted(context.Background(), store.PostedMessage{
		BindingID: "b", Identity: "sol", ChannelID: "c1", MessageID: "m1",
		StateHash: "h1", PostedAt: at, LastSeenAt: at,
	}))

	res, err := p.ClearHistory(context.Background(), "b")
	require.NoError(t, err)
	require.Equal(t, 0, res.RowsPurged, "rows preserved when Discord deletes fail")
	rows, _ := st.GetPosted(context.Background(), "b")
	require.Len(t, rows, 1)
	require.True(t, res.BindingEnabled, "disabled tombstone is still cleared independently")
}

// sortableItem is a stub Item that implements features.Sortable. Used by the
// reorder-on-rank-change test below.
type sortableItem struct {
	*stubItem
	key int64
}

func (s *sortableItem) SortKey() int64 { return s.key }

func mkSortable(id, title string, key int64) *sortableItem {
	return &sortableItem{stubItem: mkItem(id, title), key: key}
}

func TestPublisher_PostsInPriceAscendingOrder(t *testing.T) {
	// Items A=100, B=200, C=300. After Apply, the Discord posts must arrive
	// in that ascending order — that's how the channel ends up cheapest-at-
	// top, priciest-at-bottom.
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	a := mkSortable("system:A", "A", 100)
	b := mkSortable("system:B", "B", 200)
	c := mkSortable("system:C", "C", 300)

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	_, err := p.Apply(context.Background(), bnd(), features.Snapshot{
		Items: []features.Item{c, a, b}, Healthy: true, GeneratedAt: t0,
	})
	require.NoError(t, err)

	posts := dc.PostCalls()
	require.Len(t, posts, 3)
	require.Equal(t, "A", posts[0].Embed.Title, "cheapest posted first (oldest in channel)")
	require.Equal(t, "B", posts[1].Embed.Title)
	require.Equal(t, "C", posts[2].Embed.Title, "priciest posted last (newest in channel)")
}

func TestPublisher_RebuildAlwaysReorders(t *testing.T) {
	// Three systems with prices 100 / 200 / 300 — channel order should be
	// A (cheapest, oldest message) → B → C (priciest, newest). Then A's
	// price jumps to 500. New order: B → C → A. Algorithm should:
	//   - Edit-in-place B and C (still at the front of desired order, in same
	//     relative position).
	// Wait — actually B was second, now first; C was third, now second; A
	// was first, now third. So divergeAt is 0 (first slot was A, now B).
	// → all three reposted.
	//
	// But if instead A → 50 (still cheapest), nothing moves; all three are
	// edits/noops.
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	a := mkSortable("system:A", "A", 100)
	b := mkSortable("system:B", "B", 200)
	c := mkSortable("system:C", "C", 300)

	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	_, err := p.Apply(context.Background(), bnd(), features.Snapshot{
		Items: []features.Item{c, a, b}, Healthy: true, GeneratedAt: t0,
	})
	require.NoError(t, err)
	require.Len(t, dc.PostCalls(), 3, "first cycle posts all three")

	require.Len(t, dc.PostCalls(), 3, "first cycle posts all three in price-ascending order")
	dc.Reset()

	// A's price jumps to 500 → new order is B / C / A. Wipe-and-rebuild
	// always issues N deletes + N posts regardless of how the rank moved.
	a.key = 500
	t1 := t0.Add(time.Hour)
	_, err = p.Apply(context.Background(), bnd(), features.Snapshot{
		Items: []features.Item{a, b, c}, Healthy: true, GeneratedAt: t1,
	})
	require.NoError(t, err)
	require.Len(t, dc.DeleteCalls(), 3, "all three prior messages wiped")
	posts := dc.PostCalls()
	require.Len(t, posts, 3, "all three reposted in the new order")
	require.Equal(t, "B", posts[0].Embed.Title)
	require.Equal(t, "C", posts[1].Embed.Title)
	require.Equal(t, "A", posts[2].Embed.Title, "A is now priciest, posted last")
}
