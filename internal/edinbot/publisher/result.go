package publisher

// ItemAction enumerates the publisher's possible decisions per item.
type ItemAction string

const (
	ActionPost     ItemAction = "post"
	ActionEdit     ItemAction = "edit"
	ActionStrike   ItemAction = "strike"
	ActionUnstrike ItemAction = "unstrike"
	ActionNoop     ItemAction = "noop"
)

// ItemResult carries the per-item outcome of one Apply() call.
//   - Err nil   → the action succeeded and persistence is consistent
//   - Err non-nil → the action was attempted and failed; persistence
//     reflects the unchanged previous state for this item
type ItemResult struct {
	Identity string
	Action   ItemAction
	Err      error
}

// Result aggregates ItemResults plus per-action counters for ergonomic
// metrics/logging.
type Result struct {
	Items                                          []ItemResult
	Posted, Edited, Struck, Unstruck, Noop, Failed int
}

// Tally re-derives the counters from Items. Caller responsibility — the
// publisher invokes this once at end of Apply().
func (r *Result) Tally() {
	for _, it := range r.Items {
		if it.Err != nil {
			r.Failed++
			continue
		}
		switch it.Action {
		case ActionPost:
			r.Posted++
		case ActionEdit:
			r.Edited++
		case ActionStrike:
			r.Struck++
		case ActionUnstrike:
			r.Unstruck++
		case ActionNoop:
			r.Noop++
		}
	}
}
