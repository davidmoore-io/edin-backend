package features

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
)

type LTDAlerts struct {
	client  *controlclient.Client
	retries []time.Duration
}

func NewLTDAlerts(client *controlclient.Client) *LTDAlerts {
	return &LTDAlerts{
		client:  client,
		retries: []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute},
	}
}

func (l *LTDAlerts) SetRetryIntervals(intervals []time.Duration) { l.retries = intervals }

func (l *LTDAlerts) Name() string          { return "ltd-alerts" }
func (l *LTDAlerts) DefaultConfig() Config { return Config{} }
func (l *LTDAlerts) Validate(c Config) error {
	for k := range c {
		return fmt.Errorf("unknown config key %q (no keys supported in MVP)", k)
	}
	return nil
}

func (l *LTDAlerts) Poll(ctx context.Context, c Config) (Snapshot, error) {
	resp, err := l.fetchWithRetry(ctx)
	if err != nil {
		return Snapshot{Healthy: false}, err
	}
	if !resp.IsStructurallyValid() {
		return Snapshot{Healthy: false}, fmt.Errorf("ltd response structurally invalid")
	}
	now := resp.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// A station can appear in several MiningMap entries because mining bubbles
	// from different Fortified/Stronghold systems overlap. Dedup by
	// (system, station) so we don't render the same line N times. Also drop
	// "Orbital Construction Site: …" stations — they show up in the raw feed
	// but aren't actionable trade targets (cargo can't be delivered there).
	bySys := map[string][]controlclient.Buyer{}
	mapURLBySys := map[string][]string{}
	seen := map[string]bool{}
	for _, m := range resp.Maps {
		for _, b := range m.Buyers {
			if isExcludedStation(b.StationName) {
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
		items = append(items, buildLTDItem(sys, buyers, firstMapURL(mapURLBySys[sys])))
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

func (l *LTDAlerts) fetchWithRetry(ctx context.Context) (*controlclient.LTDBuyersResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= len(l.retries); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(l.retries[attempt-1]):
			}
		}
		resp, err := l.client.LTDBuyers(ctx)
		if err == nil {
			if attempt > 0 {
				log.Printf("[INFO] ltd: fetch succeeded on attempt %d", attempt+1)
			}
			return resp, nil
		}
		log.Printf("[WARN] ltd: fetch attempt %d/%d failed: %v",
			attempt+1, len(l.retries)+1, err)
		lastErr = err
	}
	return nil, fmt.Errorf("ltd fetch exhausted retries: %w", lastErr)
}

type ltdItem struct {
	system string
	buyers []controlclient.Buyer
	mapURL string
	hash   string
}

func buildLTDItem(system string, buyers []controlclient.Buyer, mapURL string) *ltdItem {
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
		fmt.Fprintf(h, "stn=%s|p=%d|d=%d|sc=%.0f|kp=%.3f|fs=%s|mu=%d|bu=%d\n",
			b.StationName, b.LTDPrice, b.LTDDemand, b.Score, kp, b.FactionState,
			unixOrZero(b.MarketUpdatedAt), unixOrZero(b.BGSUpdatedAt))
	}
	return &ltdItem{
		system: system,
		buyers: buyers,
		mapURL: mapURL,
		hash:   hex.EncodeToString(h.Sum(nil)),
	}
}

// BuildLTDItemForTest is exposed so tests can construct items directly.
func BuildLTDItemForTest(system string, buyers []controlclient.Buyer) Item {
	return buildLTDItem(system, buyers, "")
}

func (l *ltdItem) Identity() string  { return "system:" + l.system }
func (l *ltdItem) StateHash() string { return l.hash }

func (l *ltdItem) Render() *discordgo.MessageEmbed {
	tier := topTier(l.buyers)

	state := ""
	var kainePct float64
	if len(l.buyers) > 0 {
		state = l.buyers[0].FactionState
		if kp := l.buyers[0].KaineProgress; kp != nil {
			kainePct = *kp * 100
		}
	}

	var desc strings.Builder
	if state != "" {
		fmt.Fprintf(&desc, "%s · ", state)
	}
	fmt.Fprintf(&desc, "Kaine %.1f%% · %d station(s)", kainePct, len(l.buyers))
	desc.WriteString("\n")
	if l.mapURL != "" {
		fmt.Fprintf(&desc, "-# [Map](%s)\n", l.mapURL)
	}

	rows := make([]renderRow, len(l.buyers))
	for i, b := range l.buyers {
		var ltd string
		if b.LTDDemand > 0 && b.LTDPrice > 0 {
			ltd = fmt.Sprintf("LTD %st @%sk", commaInt(b.LTDDemand), kInt(b.LTDPrice))
		} else if b.LTDDemand > 0 {
			ltd = fmt.Sprintf("LTD %st", commaInt(b.LTDDemand))
		}
		rows[i] = renderRow{
			Station: b.StationName,
			Cells:   []string{ltd},
			Seen:    seenShort(b),
		}
	}
	table, truncated := renderTable(rows, 5)
	desc.WriteString(table)
	if truncated > 0 {
		fmt.Fprintf(&desc, "\n+ %d more — open in [Kaine](%s)", truncated, edsmURL(l.system))
	}

	return &discordgo.MessageEmbed{
		Title:       tierEmoji(tier) + " " + l.system,
		Description: desc.String(),
		URL:         edsmURL(l.system),
		Color:       tierColor(tier),
	}
}
