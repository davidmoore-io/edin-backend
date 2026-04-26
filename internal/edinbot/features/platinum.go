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
	bySys := map[string][]controlclient.Buyer{}
	for _, m := range resp.Maps {
		for _, b := range m.Buyers {
			if !platinumBuyerHasPricing(b) {
				continue
			}
			bySys[b.SystemName] = append(bySys[b.SystemName], b)
		}
	}

	items := make([]Item, 0, len(bySys))
	for sys, buyers := range bySys {
		items = append(items, buildPlatinumItem(sys, buyers))
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
	system string
	buyers []controlclient.Buyer
	hash   string
}

func buildPlatinumItem(system string, buyers []controlclient.Buyer) *platinumItem {
	sort.Slice(buyers, func(i, j int) bool {
		return buyers[i].StationName < buyers[j].StationName
	})

	h := sha256.New()
	fmt.Fprintf(h, "system=%s\n", system)
	for _, b := range buyers {
		var kp float64
		if b.KaineProgress != nil {
			kp = *b.KaineProgress
		}
		fmt.Fprintf(h, "stn=%s|pp=%d|pd=%d|op=%d|od=%d|sc=%.0f|kp=%.3f|fs=%s\n",
			b.StationName, b.PlatinumPrice, b.PlatinumDemand, b.OsmiumPrice, b.OsmiumDemand,
			b.Score, kp, b.FactionState)
	}
	return &platinumItem{
		system: system,
		buyers: buyers,
		hash:   hex.EncodeToString(h.Sum(nil)),
	}
}

// BuildPlatinumItemForTest is exposed so tests can construct items directly.
func BuildPlatinumItemForTest(system string, buyers []controlclient.Buyer) Item {
	return buildPlatinumItem(system, buyers)
}

func (p *platinumItem) Identity() string  { return "system:" + p.system }
func (p *platinumItem) StateHash() string { return p.hash }

func (p *platinumItem) Render() *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:       p.system,
		Description: fmt.Sprintf("**Platinum buyers** — %d station(s) in Boom", len(p.buyers)),
		URL:         "https://www.edsm.net/en/system?systemName=" + strings.ReplaceAll(p.system, " ", "+"),
		Color:       0x9CA3AF, // slate
	}
	for _, b := range p.buyers {
		val := strings.Builder{}
		fmt.Fprintf(&val, "**%s** — %s · score %.0f", b.StationName, b.FactionState, b.Score)
		if b.PlatinumDemand > 0 && b.PlatinumPrice > 0 {
			fmt.Fprintf(&val, "\n• Pt: %s t @ %sk", commaInt(b.PlatinumDemand), kInt(b.PlatinumPrice))
		}
		if b.OsmiumDemand > 0 && b.OsmiumPrice > 0 {
			fmt.Fprintf(&val, "\n• Os: %s t @ %sk", commaInt(b.OsmiumDemand), kInt(b.OsmiumPrice))
		}
		var kp float64
		if b.KaineProgress != nil {
			kp = *b.KaineProgress * 100 // stored as 0..1; render as percent
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("Kaine %.1f%%", kp),
			Value: val.String(),
		})
	}
	return embed
}

// platinumBuyerHasPricing returns true when at least one of the two commodities
// (platinum, osmium) has BOTH a non-zero price AND a non-zero demand. Buyers
// without pricing are dropped from the snapshot — operators want only
// actionable targets, not stations whose price feed has lapsed.
func platinumBuyerHasPricing(b controlclient.Buyer) bool {
	if b.PlatinumPrice > 0 && b.PlatinumDemand > 0 {
		return true
	}
	if b.OsmiumPrice > 0 && b.OsmiumDemand > 0 {
		return true
	}
	return false
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
