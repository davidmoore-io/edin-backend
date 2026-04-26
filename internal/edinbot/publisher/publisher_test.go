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

func (m *memStore) RecordPollCycle(ctx context.Context, c store.PollCycle) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cycles = append(m.cycles, c)
	return nil
}

func (m *memStore) RecordDiagnoseReport(ctx context.Context, r store.DiagnoseReport) error {
	return nil
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

func TestPublisher_UnchangedHash_Noop(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	item := mkItem("system:Sol", "Sol")
	_, err := p.Apply(context.Background(), bnd(), snap(item))
	require.NoError(t, err)
	dc.Reset()

	res, err := p.Apply(context.Background(), bnd(), snap(item))
	require.NoError(t, err)
	require.Equal(t, 1, res.Noop)
	require.Empty(t, dc.PostCalls())
	require.Empty(t, dc.EditCalls(), "noop must NOT call Discord")
}

func TestPublisher_ChangedHash_Edits(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	_, err := p.Apply(context.Background(), bnd(), snap(mkItem("system:Sol", "Sol-v1")))
	require.NoError(t, err)
	dc.Reset()

	res, err := p.Apply(context.Background(), bnd(), snap(mkItem("system:Sol", "Sol-v2")))
	require.NoError(t, err)
	require.Equal(t, 1, res.Edited)
	require.Len(t, dc.EditCalls(), 1)
	require.Empty(t, dc.PostCalls())
}

func TestPublisher_DisappearedItem_Strikes(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	a := mkItem("system:A", "A")
	b := mkItem("system:B", "B")
	_, err := p.Apply(context.Background(), bnd(), snap(a, b))
	require.NoError(t, err)
	dc.Reset()

	// B disappears.
	res, err := p.Apply(context.Background(), bnd(), snap(a))
	require.NoError(t, err)
	require.Equal(t, 1, res.Struck)
	require.Equal(t, 1, res.Noop, "A should noop")
	require.Len(t, dc.EditCalls(), 1, "strike is implemented as an edit")

	posted, _ := st.GetPosted(context.Background(), "test-binding")
	require.NotNil(t, posted["system:B"].StruckAt)
}

func TestPublisher_ReappearedItem_Unstrikes(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	item := mkItem("system:A", "A")
	_, _ = p.Apply(context.Background(), bnd(), snap(item))
	_, _ = p.Apply(context.Background(), bnd(), snap()) // disappears, struck
	dc.Reset()

	res, err := p.Apply(context.Background(), bnd(), snap(item))
	require.NoError(t, err)
	require.Equal(t, 1, res.Unstruck)
	require.Len(t, dc.EditCalls(), 1)

	posted, _ := st.GetPosted(context.Background(), "test-binding")
	require.Nil(t, posted["system:A"].StruckAt, "struck_at must clear after unstrike")
	require.NotNil(t, posted["system:A"].UnstruckAt)
}

func TestPublisher_PartialFailure_DoesNotAbortOthers(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	a := mkItem("system:A", "A")
	b := mkItem("system:B", "B")
	_, _ = p.Apply(context.Background(), bnd(), snap(a, b))
	dc.Reset()

	dc.EditErr = errors.New("temporary 500")

	a2 := mkItem("system:A", "A-v2")
	b2 := mkItem("system:B", "B-v2")
	res, err := p.Apply(context.Background(), bnd(), snap(a2, b2))
	require.NoError(t, err, "partial failure does NOT abort the cycle")
	require.Equal(t, 2, res.Failed, "both edits failed because the fake fails ALL edits")

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

	res, err := p.Apply(context.Background(), bnd(), snap(mkItem("system:A", "A")))
	require.NoError(t, err)
	require.Equal(t, 1, res.Failed)

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

func TestPublisher_UsesSnapshotGeneratedAtForTimestamps(t *testing.T) {
	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	p := publisher.New(st, dc)

	item := mkItem("system:A", "A")
	_, _ = p.Apply(context.Background(), bnd(), snap(item))

	frozenTime := time.Date(2030, 1, 1, 12, 34, 0, 0, time.UTC)
	strikeSnap := features.Snapshot{Items: nil, Healthy: true, GeneratedAt: frozenTime}
	_, _ = p.Apply(context.Background(), bnd(), strikeSnap)

	require.Len(t, dc.EditCalls(), 1)
	require.Contains(t, dc.EditCalls()[0].Embed.Footer.Text, "12:34 UTC",
		"strike footer must use Snapshot.GeneratedAt, not wall clock")
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

// Phase 10.3: Restart safety — pre-existing posted_messages row continues to
// be edited in place after a (simulated) bot restart. No double-posts.
func TestPublisher_RestartScenario_ContinuesEditingExistingMessages(t *testing.T) {
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
	require.Equal(t, 1, res.Edited, "must edit, NOT post — message_id preserved across restart")

	require.Empty(t, dc.PostCalls(), "no new post — recovery uses existing message_id")
	require.Len(t, dc.EditCalls(), 1)
	require.Equal(t, "pre-existing-msg-42", dc.EditCalls()[0].MessageID)
}
