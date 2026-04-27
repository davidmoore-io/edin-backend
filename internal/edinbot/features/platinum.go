package features

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
)

// PlatinumBoomAlerts is the PollFeature that emits one Item per Boom-state
// buyer SYSTEM (not per station). Per spec §1 decision 2.
type PlatinumBoomAlerts struct {
	client  *controlclient.Client
	retries []time.Duration
}

func NewPlatinumBoomAlerts(client *controlclient.Client) *PlatinumBoomAlerts {
	return &PlatinumBoomAlerts{
		client:  client,
		retries: []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute},
	}
}

// SetRetryIntervals lets tests speed up the retry loop without changing
// production behaviour.
func (p *PlatinumBoomAlerts) SetRetryIntervals(intervals []time.Duration) { p.retries = intervals }

func (p *PlatinumBoomAlerts) Name() string          { return "platinum-boom-alerts" }
func (p *PlatinumBoomAlerts) DefaultConfig() Config { return Config{} }
func (p *PlatinumBoomAlerts) Validate(c Config) error {
	for k := range c {
		return fmt.Errorf("unknown config key %q (no keys supported in MVP)", k)
	}
	return nil
}

func (p *PlatinumBoomAlerts) Poll(ctx context.Context, c Config) (Snapshot, error) {
	resp, err := p.fetchWithRetry(ctx)
	if err != nil {
		return Snapshot{Healthy: false}, err
	}
	if !resp.IsStructurallyValid() {
		return Snapshot{Healthy: false}, fmt.Errorf("plasmium response structurally invalid")
	}

	now := resp.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// Group buyers by buyer-SYSTEM (per spec decision 2).
	// A station can appear in several MiningMap entries because mining bubbles
	// from different Fortified/Stronghold systems overlap. Dedup by
	// (system, station) so we don't render the same line N times. Also drop
	// "Orbital Construction Site: …" stations — they show up in the raw feed
	// but aren't actionable trade targets (cargo can't be delivered there).
	bySys := map[string][]controlclient.Buyer{}
	mapURLBySys := map[string][]string{} // first-non-empty wins; preserve order so dedup is deterministic
	seen := map[string]bool{}
	for _, m := range resp.Maps {
		for _, b := range m.Buyers {
			if isExcludedStation(b.StationName) {
				continue
			}
			if !isFresh(b, now) {
				// Stale or never-seen market data — drop. Operators don't
				// want to chase alerts whose price feed has lapsed > 24h.
				continue
			}
			key := b.SystemName + "\x00" + b.StationName
			if seen[key] {
				continue
			}
			seen[key] = true
			bySys[b.SystemName] = append(bySys[b.SystemName], b)
			mapURLBySys[b.SystemName] = append(mapURLBySys[b.SystemName], m.Map1)
		}
	}

	items := make([]Item, 0, len(bySys))
	for sys, buyers := range bySys {
		items = append(items, buildPlatinumItem(sys, buyers, firstMapURL(mapURLBySys[sys])))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Identity() < items[j].Identity() })

	return Snapshot{
		Items:       items,
		Healthy:     true,
		GeneratedAt: now,
		SourceMeta: map[string]any{
			"total_maps":   resp.TotalMaps,
			"total_buyers": resp.TotalBuyers,
		},
	}, nil
}

func (p *PlatinumBoomAlerts) fetchWithRetry(ctx context.Context) (*controlclient.PlasmiumBuyersResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= len(p.retries); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(p.retries[attempt-1]):
			}
		}
		resp, err := p.client.PlasmiumBuyers(ctx)
		if err == nil {
			if attempt > 0 {
				log.Printf("[INFO] platinum: fetch succeeded on attempt %d", attempt+1)
			}
			return resp, nil
		}
		log.Printf("[WARN] platinum: fetch attempt %d/%d failed: %v",
			attempt+1, len(p.retries)+1, err)
		lastErr = err
	}
	return nil, fmt.Errorf("plasmium fetch exhausted retries: %w", lastErr)
}

// platinumItem is the Item implementation for one buyer system.
type platinumItem struct {
	system  string
	buyers  []controlclient.Buyer
	mapURL  string
	hash    string
}

func buildPlatinumItem(system string, buyers []controlclient.Buyer, mapURL string) *platinumItem {
	// Highest score first; alphabetical station name as a stable tiebreaker.
	sort.Slice(buyers, func(i, j int) bool {
		if buyers[i].Score != buyers[j].Score {
			return buyers[i].Score > buyers[j].Score
		}
		return buyers[i].StationName < buyers[j].StationName
	})

	h := sha256.New()
	fmt.Fprintf(h, "system=%s|map=%s\n", system, mapURL)
	for _, b := range buyers {
		var kp float64
		if b.KaineProgress != nil {
			kp = *b.KaineProgress
		}
		fmt.Fprintf(h, "stn=%s|pp=%d|pd=%d|op=%d|od=%d|sc=%.0f|kp=%.3f|fs=%s|mu=%d|bu=%d\n",
			b.StationName, b.PlatinumPrice, b.PlatinumDemand, b.OsmiumPrice, b.OsmiumDemand,
			b.Score, kp, b.FactionState, unixOrZero(b.MarketUpdatedAt), unixOrZero(b.BGSUpdatedAt))
	}
	return &platinumItem{
		system: system,
		buyers: buyers,
		mapURL: mapURL,
		hash:   hex.EncodeToString(h.Sum(nil)),
	}
}

// BuildPlatinumItemForTest is exposed so tests can construct items directly.
func BuildPlatinumItemForTest(system string, buyers []controlclient.Buyer) Item {
	return buildPlatinumItem(system, buyers, "")
}

func (p *platinumItem) Identity() string  { return "system:" + p.system }
func (p *platinumItem) StateHash() string { return p.hash }

func (p *platinumItem) Render() *discordgo.MessageEmbed {
	tier := topTier(p.buyers)

	state := ""
	var kainePct float64
	if len(p.buyers) > 0 {
		state = p.buyers[0].FactionState
		if kp := p.buyers[0].KaineProgress; kp != nil {
			kainePct = *kp * 100
		}
	}

	// Description-line-1 carries the title row: 🟢 SystemName · [Map].
	// Embed.title is left empty because Discord titles don't render masked
	// links — we want the [Map] to be clickable inline. ### makes the line
	// render at heading size on clients that support embed-headings.
	var desc strings.Builder
	fmt.Fprintf(&desc, "### %s [%s](%s)", tierEmoji(tier), p.system, edsmURL(p.system))
	if p.mapURL != "" {
		fmt.Fprintf(&desc, " · [Map](%s)", p.mapURL)
	}
	desc.WriteString("\n")
	if state != "" {
		fmt.Fprintf(&desc, "%s · ", state)
	}
	fmt.Fprintf(&desc, "Kaine %.1f%% · %d station(s)\n", kainePct, len(p.buyers))

	blocks := make([]stationBlock, len(p.buyers))
	for i, b := range p.buyers {
		var body []string
		if b.PlatinumDemand > 0 || b.PlatinumPrice > 0 {
			body = append(body, fmt.Sprintf("Pt — Price: %s / Demand: %s",
				fullPrice(b.PlatinumPrice), fullDemand(b.PlatinumDemand)))
		}
		if b.OsmiumDemand > 0 || b.OsmiumPrice > 0 {
			body = append(body, fmt.Sprintf("Os — Price: %s / Demand: %s",
				fullPrice(b.OsmiumPrice), fullDemand(b.OsmiumDemand)))
		}
		blocks[i] = stationBlock{
			Header:     b.StationName,
			Body:       body,
			SeenAtUnix: unixOrZero(b.MarketUpdatedAt),
		}
	}
	table, truncated := renderStationBlocks(blocks, 5)
	desc.WriteString(table)
	if truncated > 0 {
		fmt.Fprintf(&desc, "\n+ %d more", truncated)
	}

	return &discordgo.MessageEmbed{
		Description: desc.String(),
		Color:       tierColor(tier),
	}
}

// lastSeenStamp picks the most-relevant freshness timestamp for a buyer and
// formats it as Discord's live-relative <t:N:R> token ("5 minutes ago").
// MarketUpdatedAt is preferred because the message content is commodity
// pricing — operators want to know how stale the price feed is. If market
// data is missing, BGSUpdatedAt is shown as a fallback (still useful: tells
// you when faction state was last refreshed). Returns "" when neither is set.
// isExcludedStation returns true for station-name patterns that the bot
// should never surface as alerts. Orbital Construction Sites in particular
// appear in the kaine API response but cargo can't be delivered to them —
// listing them is purely noise.
func isExcludedStation(name string) bool {
	return strings.HasPrefix(name, "Orbital Construction Site")
}

func unixOrZero(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.Unix()
}

// seenShort returns a compact human-readable freshness label like "5m", "2h",
// "3d", "2w" computed against the snapshot's time. Code blocks (which we use
// for the buyer table for alignment) don't render Discord's <t:N:R> token,
// so this is the static-text fallback. Outside the code block we still use
// <t:N:R> for live-updating timestamps.
//
// The snapshot's GeneratedAt would be a more correct anchor than time.Now()
// — TODO if drift becomes visible. For a 15-min poll cadence the difference
// is sub-perceptible.
func seenShort(b controlclient.Buyer) string {
	t := b.MarketUpdatedAt
	suffix := ""
	if t == nil || t.IsZero() {
		t = b.BGSUpdatedAt
		suffix = "*" // BGS-derived freshness, marked so operators know it isn't market data
	}
	if t == nil || t.IsZero() {
		return ""
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return "now" + suffix
	case d < time.Hour:
		return fmt.Sprintf("%dm%s", int(d.Minutes()), suffix)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%s", int(d.Hours()), suffix)
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd%s", int(d.Hours()/24), suffix)
	default:
		return fmt.Sprintf("%dw%s", int(d.Hours()/(24*7)), suffix)
	}
}

func lastSeenStamp(b controlclient.Buyer) string {
	if b.MarketUpdatedAt != nil && !b.MarketUpdatedAt.IsZero() {
		return fmt.Sprintf("<t:%d:R>", b.MarketUpdatedAt.Unix())
	}
	if b.BGSUpdatedAt != nil && !b.BGSUpdatedAt.IsZero() {
		return fmt.Sprintf("<t:%d:R> (BGS)", b.BGSUpdatedAt.Unix())
	}
	return ""
}

func commaInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func kInt(n int64) string {
	return strconv.FormatInt(n/1000, 10)
}
