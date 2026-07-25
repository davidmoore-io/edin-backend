// Package galaxy holds primitives shared across galaxy consumers. It
// currently exposes the canonical slug rule for system
// names; could grow to hold faction/power lookup helpers if a second shared
// concern surfaces.
package galaxy

import "strings"

// Slugify converts a system name into the URL-safe slug we use as the stable
// identifier for permalinks (https://edin.space/kaine/systems/<slug>) and
// the lookup key for relational system-name resolution.
//
// The rule is deliberately minimal: trim outer whitespace, then remove
// all internal spaces. Dashes — which appear naturally in many ED system
// names like "Col 359 Sector LL-J b11-3" — are preserved.
//
// Why so simple:
//   - URL-safe without percent-escaping (operator brief: no "%" in URLs).
//   - One-way derivation; we look up by slug, never reverse-translate.
//   - Stable across the whole stack — eddn-listener writes the slug from
//     the system name, control-API reads back by slug, the bot calls
//     control-API with whatever the user typed slug'd.
//
// Edge cases we deliberately accept:
//   - Two systems whose names differ only by spacing collide. ED system
//     names are largely procedural and unique under the existing naming
//     rules; an actual collision is vanishingly rare, and the call-site
//     (Phase 2 endpoint) has a documented branch to handle it.
//   - Inner whitespace runs collapse to nothing. "  HIP   61332  " →
//     "HIP61332", same as "HIP 61332".
func Slugify(systemName string) string {
	return strings.ReplaceAll(strings.TrimSpace(systemName), " ", "")
}
