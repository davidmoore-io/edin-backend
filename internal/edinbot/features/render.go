package features

import (
	"fmt"
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
)

// Tier groups buyers by score band into one of three visual buckets. The
// bands are coarse on purpose — operators want at-a-glance "is this worth
// looking at" first, fine-grained number reading second. The thresholds
// were picked from a sample of live data: scores cluster around 0–10, ~40,
// and 75–100, so 80/30 splits the population into roughly equal-thirds.
type Tier int

const (
	TierLow Tier = iota
	TierMid
	TierHigh
)

func tierOf(score float64) Tier {
	switch {
	case score >= 80:
		return TierHigh
	case score >= 30:
		return TierMid
	default:
		return TierLow
	}
}

// tierEmoji returns the leading emoji used in the embed title to give the
// channel a single colour-coded scan signal per system. The same tier also
// drives the embed's coloured side-bar (see tierColor).
func tierEmoji(t Tier) string {
	switch t {
	case TierHigh:
		return "🟢"
	case TierMid:
		return "🟡"
	default:
		return "🔴"
	}
}

// tierColor returns the embed side-bar hex colour. Pure visual signal; same
// information as the leading emoji, redundant on purpose because a stream
// of messages is scanned both vertically (colour bar) and horizontally
// (title emoji).
func tierColor(t Tier) int {
	switch t {
	case TierHigh:
		return 0x22c55e // green-500
	case TierMid:
		return 0xeab308 // amber-500
	default:
		return 0xef4444 // red-500
	}
}

// topTier returns the tier of the highest-scoring buyer. Buyers must already
// be sorted desc by score (the buildItem funcs do this).
func topTier(buyers []controlclient.Buyer) Tier {
	if len(buyers) == 0 {
		return TierLow
	}
	return tierOf(buyers[0].Score)
}

// edsmURL returns a clickable EDSM link for the buyer's system. Used as the
// embed.URL so the title is itself a click-target.
func edsmURL(systemName string) string {
	return "https://www.edsm.net/en/system?systemName=" + strings.ReplaceAll(systemName, " ", "+")
}

// MaxFreshness is the maximum age of MarketUpdatedAt for a buyer to qualify
// for posting. Any buyer whose price feed has been silent for longer is
// dropped before the snapshot is built — operators don't want to chase
// alerts whose underlying data hasn't ticked in over a day. Buyers with
// no MarketUpdatedAt at all are also dropped (no freshness signal at all).
const MaxFreshness = 24 * time.Hour

// isFresh returns true if the buyer's market data was seen within MaxFreshness.
// Used by both plat and LTD features to gate which buyers reach the rendered
// snapshot. The "now" anchor is the snapshot's GeneratedAt to keep the
// decision deterministic across the cycle.
func isFresh(b controlclient.Buyer, now time.Time) bool {
	if b.MarketUpdatedAt == nil || b.MarketUpdatedAt.IsZero() {
		return false
	}
	return now.Sub(*b.MarketUpdatedAt) <= MaxFreshness
}

// firstMapURL returns the first non-empty Map1 URL the API returned for any
// bubble whose buyers landed in this system. There can be several overlapping
// bubbles per system; we deliberately pick whichever the API listed first
// (typically the closest Fortified/Stronghold). Empty string when no map URL
// is available — render path then omits the "Map" link entirely.
func firstMapURL(urls []string) string {
	for _, u := range urls {
		if u != "" {
			return u
		}
	}
	return ""
}

// stationBlock is one buyer rendered as a multi-line block inside the code
// block. Each Line is exactly one rendered line; the renderer joins them with
// "\n" and separates blocks with one blank line.
//
// Layout the bot ships today:
//   StationName · 5m
//   Price: 245,000c / Demand: 38,209t           (LTD — single line)
//
// or for plat (two commodities):
//   StationName · 5m
//   Pt — Price: 245,000c / Demand: 38,209t
//   Os — Price: 199,000c / Demand: 439,390t
//
// Per-station block layout favours readability over horizontal column
// alignment: each station stands on its own, repeated structure across
// stations is what creates the visual rhythm of the channel.
type stationBlock struct {
	// Header is rendered bold. Just the station name now — freshness moved
	// to its own line so the <t:N:R> token can live-update.
	Header string
	// One body line per commodity. Empty body is allowed.
	Body []string
	// SeenAtUnix drives the "Seen: <t:N:R>" line. 0 omits the line.
	SeenAtUnix int64
}

// renderStationBlocks emits a markdown-formatted string containing the given
// per-station blocks, capped to maxBlocks. Returns the rendered string plus
// the count of stations that didn't fit.
//
// Markdown (not code-block) so Discord's <t:N:R> live-relative timestamps
// render — operators see "Seen: 5 minutes ago" updating client-side without
// the bot ever having to edit the message. Tradeoff: lose monospace
// alignment, but per-station blocks don't need column alignment between
// stations anyway (each block stands alone).
//
// Block layout produced:
//   **StationName**
//   <each Body line — typically Price/Demand>
//   Seen: <t:N:R>
//
// Separated by a blank line between stations.
func renderStationBlocks(blocks []stationBlock, maxBlocks int) (string, int) {
	if len(blocks) == 0 {
		return "", 0
	}
	if maxBlocks <= 0 || maxBlocks > len(blocks) {
		maxBlocks = len(blocks)
	}
	visible := blocks[:maxBlocks]
	truncated := len(blocks) - maxBlocks

	var b strings.Builder
	for i, blk := range visible {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("**")
		b.WriteString(blk.Header)
		b.WriteString("**")
		for _, line := range blk.Body {
			b.WriteByte('\n')
			b.WriteString(line)
		}
		if blk.SeenAtUnix > 0 {
			fmt.Fprintf(&b, "\nSeen: <t:%d:R>", blk.SeenAtUnix)
		}
	}
	return b.String(), truncated
}

// fullPrice formats a credit value with thousands separators and a trailing
// "c" suffix — e.g. 245000 → "245,000c". Used inside the code block where
// monospace makes wide numerals legible.
func fullPrice(n int64) string {
	return commaInt64(n) + "c"
}

// fullDemand formats a tonnage with thousands separators and a "t" suffix.
func fullDemand(n int64) string {
	return commaInt64(n) + "t"
}

// commaInt64 is the local copy of the existing commaInt — defined here to
// avoid a forward dependency on the platinum.go-defined helper from
// shared render code.
func commaInt64(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := ""
	for {
		if n < 1000 {
			s = itoa(n) + s
			break
		}
		s = "," + pad3(n%1000) + s
		n /= 1000
	}
	if neg {
		s = "-" + s
	}
	return s
}

func itoa(n int64) string {
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

func pad3(n int64) string {
	s := itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

