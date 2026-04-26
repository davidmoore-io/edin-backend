package features

// Registry is the single source of truth for "what features exist". Populated
// by cmd/edin-bot/main.go at startup, after dependencies are wired. Concrete
// features do NOT register themselves via init() — that would force them to
// hold nil clients. main.go assigns explicit, fully-constructed values.
//
// Each value MUST implement Feature plus exactly one of PollFeature /
// EventDrivenFeature. The registry test in registry_test.go asserts this
// invariant.
//
// Initial state: empty. Populated in Phase 13 (main.go).
var Registry = map[string]Feature{}
