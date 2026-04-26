package features

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// OpsBusEvent is the in-package mirror of scheduler.OpsEvent. Defined here to
// avoid a features→scheduler import cycle. cmd/edin-bot/main.go bridges the
// two by passing scheduler.OpsBus.Subscribe via a closure that converts.
type OpsBusEvent struct {
	BindingID  string
	Reason     string
	Attempts   int
	Error      error
	Report     map[string]any
	OccurredAt time.Time
}

// PriorOutage carries the persisted state of one ongoing outage. main.go
// queries discord.posted_messages for non-struck identities matching
// "outage:*", parses them, and feeds them via LoadPriorOutages BEFORE
// calling Subscribe.
type PriorOutage struct {
	Identity  string
	BindingID string
	Reason    string
	StartedAt time.Time
}

// OpsHealthAlerts implements EventDrivenFeature. Subscribes to the in-process
// ops bus (passed via constructor) and emits snapshots that fold continuous
// outages into a single ongoing message per (binding_id, reason).
type OpsHealthAlerts struct {
	subscribeFn func() <-chan OpsBusEvent

	priorOnce    sync.Once
	priorOutages map[string]PriorOutage
}

func NewOpsHealthAlerts(subscribeFn func() <-chan OpsBusEvent) *OpsHealthAlerts {
	return &OpsHealthAlerts{subscribeFn: subscribeFn}
}

// LoadPriorOutages must be called before Subscribe. Each prior outage is
// inserted into the in-memory live map so subsequent events (including
// recoveries) for the same (binding_id, reason) keep the original started_at
// and identity intact.
func (o *OpsHealthAlerts) LoadPriorOutages(prior []PriorOutage) error {
	o.priorOnce.Do(func() {
		o.priorOutages = make(map[string]PriorOutage, len(prior))
		for _, p := range prior {
			key := p.BindingID + "|" + p.Reason
			o.priorOutages[key] = p
		}
	})
	return nil
}

func (o *OpsHealthAlerts) Name() string { return "ops-health-alerts" }

func (o *OpsHealthAlerts) DefaultConfig() Config {
	return Config{
		"watch_bindings":      []any{}, // empty = watch all
		"diagnose_on_failure": true,
		"outage_dedup":        true,
	}
}

func (o *OpsHealthAlerts) Validate(c Config) error {
	allowed := map[string]bool{
		"watch_bindings": true, "diagnose_on_failure": true, "outage_dedup": true,
	}
	for k := range c {
		if !allowed[k] {
			return fmt.Errorf("unknown config key %q for ops-health-alerts", k)
		}
	}
	return nil
}

// outage tracks one ongoing failure for a (binding_id, reason). Lives in the
// in-process map until a poll_recovered event closes it.
type outage struct {
	startedAt  time.Time
	lastEvent  OpsBusEvent
	resolved   bool
	resolvedAt time.Time
}

func (o *OpsHealthAlerts) Subscribe(ctx context.Context, cfg Config) (<-chan Snapshot, error) {
	out := make(chan Snapshot, 16)
	src := o.subscribeFn()

	watch := map[string]bool{}
	if v, ok := cfg["watch_bindings"].([]any); ok {
		for _, x := range v {
			if s, ok := x.(string); ok {
				watch[s] = true
			}
		}
	}
	watchAll := len(watch) == 0

	go func() {
		defer close(out)

		var mu sync.Mutex
		live := map[string]*outage{}

		// Seed from prior outages (boot recovery).
		for k, p := range o.priorOutages {
			live[k] = &outage{
				startedAt: p.StartedAt,
				lastEvent: OpsBusEvent{BindingID: p.BindingID, Reason: p.Reason, OccurredAt: p.StartedAt},
			}
		}

		flush := func(key string) {
			mu.Lock()
			o2, ok := live[key]
			mu.Unlock()
			if !ok || o2 == nil {
				return
			}
			item := buildOpsItem(o2)
			snap := Snapshot{
				Items:       []Item{item},
				Healthy:     true,
				GeneratedAt: o2.lastEvent.OccurredAt,
			}
			if o2.resolved {
				snap.GeneratedAt = o2.resolvedAt
			}
			select {
			case out <- snap:
			case <-ctx.Done():
				return
			}
			if o2.resolved {
				mu.Lock()
				delete(live, key)
				mu.Unlock()
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-src:
				if !ok {
					return
				}
				if !watchAll && !watch[ev.BindingID] {
					continue
				}

				if ev.Reason == "poll_recovered" {
					// Close any open outage for this binding.
					mu.Lock()
					closeKeys := []string{}
					for k, o2 := range live {
						if strings.HasPrefix(k, ev.BindingID+"|") && !o2.resolved {
							o2.resolved = true
							o2.resolvedAt = ev.OccurredAt
							closeKeys = append(closeKeys, k)
						}
					}
					mu.Unlock()
					for _, k := range closeKeys {
						flush(k)
					}
					continue
				}

				key := ev.BindingID + "|" + ev.Reason
				mu.Lock()
				o2, ok := live[key]
				if !ok {
					o2 = &outage{startedAt: ev.OccurredAt}
					live[key] = o2
				}
				o2.lastEvent = ev
				mu.Unlock()
				flush(key)
			}
		}
	}()

	return out, nil
}

type opsItem struct {
	identity string
	hash     string
	embed    *discordgo.MessageEmbed
}

func buildOpsItem(o *outage) Item {
	id := fmt.Sprintf("outage:%s:%s:%s",
		o.lastEvent.BindingID, o.lastEvent.Reason,
		o.startedAt.UTC().Format(time.RFC3339),
	)

	title := fmt.Sprintf("⚠️ %s — %s", o.lastEvent.BindingID, o.lastEvent.Reason)
	desc := fmt.Sprintf("Started %s. Latest event: attempt %d.",
		o.startedAt.UTC().Format("15:04:05 UTC"), o.lastEvent.Attempts)
	color := 0xEF4444

	if o.resolved {
		title = fmt.Sprintf("✅ %s — RESOLVED", o.lastEvent.BindingID)
		desc = fmt.Sprintf("Started %s. RESOLVED at %s. Total attempts: %d.",
			o.startedAt.UTC().Format("15:04:05 UTC"),
			o.resolvedAt.UTC().Format("15:04:05 UTC"),
			o.lastEvent.Attempts,
		)
		color = 0x10B981
	}

	embed := &discordgo.MessageEmbed{
		Title:       title,
		Description: desc,
		Color:       color,
	}
	if o.lastEvent.Error != nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name: "Last error", Value: "`" + o.lastEvent.Error.Error() + "`",
		})
	}
	if o.lastEvent.Report != nil {
		var b strings.Builder
		for k, v := range o.lastEvent.Report {
			fmt.Fprintf(&b, "**%s** — %v\n", k, v)
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name: "Diagnose", Value: b.String(),
		})
	}

	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%v|%v", id, desc, o.lastEvent.Error, o.resolved)
	return &opsItem{
		identity: id,
		hash:     hex.EncodeToString(h.Sum(nil)),
		embed:    embed,
	}
}

func (o *opsItem) Identity() string                { return o.identity }
func (o *opsItem) StateHash() string               { return o.hash }
func (o *opsItem) Render() *discordgo.MessageEmbed { return o.embed }
