package features

import (
	"strings"

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

// renderRow describes one buyer row inside the code-block table. Captured
// per commodity so plat (Pt+Os) and LTD (one column) share the same
// alignment machinery.
type renderRow struct {
	Station string
	// Cells is rendered left-to-right inside fixed columns; an empty string
	// renders as blank padding so columns stay aligned with rows that DO
	// carry the data. Order is feature-defined (plat: ["Pt …", "Os …"]).
	Cells []string
	Seen  string // "5m", "2h" — rendered right-aligned for vertical scanning
}

// renderTable builds a Discord code-block formatted ASCII table. Width-stable
// across messages because every column is padded to its widest cell. Code
// blocks force monospace + truncation rules on mobile — we accept losing
// markdown bolding inside the block in exchange for the visual rhythm of
// aligned columns down a stream of similar-shaped messages (Tufte: "show
// the data, eliminate non-data ink").
//
// maxRows caps how many buyers are shown; surplus is reported as "+ N more"
// in the trailing line (returned separately so the caller can append it
// outside the code block, where masked links render).
func renderTable(rows []renderRow, maxRows int) (block string, truncated int) {
	if len(rows) == 0 {
		return "", 0
	}
	if maxRows <= 0 || maxRows > len(rows) {
		maxRows = len(rows)
	}
	visible := rows[:maxRows]
	truncated = len(rows) - maxRows

	// Determine column widths.
	stationW := 0
	cellsW := make([]int, len(visible[0].Cells))
	seenW := 0
	for _, r := range visible {
		if l := runeLen(r.Station); l > stationW {
			stationW = l
		}
		for i, c := range r.Cells {
			if l := runeLen(c); l > cellsW[i] {
				cellsW[i] = l
			}
		}
		if l := runeLen(r.Seen); l > seenW {
			seenW = l
		}
	}

	var b strings.Builder
	b.WriteString("```\n")
	for _, r := range visible {
		b.WriteString(padRight(r.Station, stationW))
		for i, c := range r.Cells {
			b.WriteString("  ")
			b.WriteString(padRight(c, cellsW[i]))
		}
		if seenW > 0 {
			b.WriteString("  ")
			b.WriteString(padLeft(r.Seen, seenW))
		}
		b.WriteByte('\n')
	}
	b.WriteString("```")
	return b.String(), truncated
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func padRight(s string, w int) string {
	if l := runeLen(s); l < w {
		return s + strings.Repeat(" ", w-l)
	}
	return s
}

func padLeft(s string, w int) string {
	if l := runeLen(s); l < w {
		return strings.Repeat(" ", w-l) + s
	}
	return s
}

