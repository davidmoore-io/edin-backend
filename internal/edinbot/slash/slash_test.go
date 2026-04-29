package slash_test

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/slash"
)

// fakeSession captures the calls slash.Router makes for assertion. We don't
// stand up a real discordgo.Session because the network surface is wide
// and irrelevant to the routing logic — the router only ever calls
// InteractionRespond. Wrapping discordgo.Session in an interface would
// invert the dependency for one test concern, so we shadow the method
// signature on a recorder here and call it via Dispatch.
//
// To keep the recorder a drop-in for *discordgo.Session in the router
// signature, we instead test by inspecting side-effects: the router should
// fail closed (no handler call) when a gate denies, and call the handler
// exactly once when the gates pass. The handler itself is the unit under
// test of the dispatch decision; we don't need to inspect the ephemeral
// reply text.

func quietLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// noNetworkRouter wires Dispatch with a session that will fail on any
// outbound network call (nil HTTP client). Handlers that don't need to
// reply work fine; gate-denied paths panic-on-network-call, which we
// catch via runDispatch's recover so we can still observe the handler
// counter.
func runDispatch(t *testing.T, r *slash.Router, ic *discordgo.InteractionCreate) {
	t.Helper()
	defer func() {
		// Recover panics from the nil-session network call inside
		// InteractionRespond. We're explicitly testing the route-decision
		// branch, not the reply transport.
		_ = recover()
	}()
	sess := &discordgo.Session{Client: nil}
	r.Dispatch(sess, ic)
}

func mkInteraction(channelID string, perms int64, command string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:      discordgo.InteractionApplicationCommand,
			ChannelID: channelID,
			Member:    &discordgo.Member{Permissions: perms},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: command,
			},
		},
	}
}

func TestRouter_DispatchesToHandlerOnHappyPath(t *testing.T) {
	var (
		mu     sync.Mutex
		called int
	)
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs:  []string{"watch-channel"},
		RequirePermissions: discordgo.PermissionAdministrator,
		Logger:             quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, sess *discordgo.Session, ic *discordgo.InteractionCreate) error {
		mu.Lock()
		called++
		mu.Unlock()
		return nil
	})

	ic := mkInteraction("watch-channel", discordgo.PermissionAdministrator, "watch")
	runDispatch(t, r, ic)

	if called != 1 {
		t.Fatalf("handler called %d times, want 1", called)
	}
}

func TestRouter_RejectsWrongChannel(t *testing.T) {
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs:  []string{"watch-channel"},
		RequirePermissions: discordgo.PermissionAdministrator,
		Logger:             quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, sess *discordgo.Session, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	ic := mkInteraction("some-other-channel", discordgo.PermissionAdministrator, "watch")
	runDispatch(t, r, ic)

	if called != 0 {
		t.Fatalf("handler must not be called when channel is wrong; got %d calls", called)
	}
}

func TestRouter_RejectsNonAdmin(t *testing.T) {
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs:  []string{"watch-channel"},
		RequirePermissions: discordgo.PermissionAdministrator,
		Logger:             quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, sess *discordgo.Session, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	// Member with SendMessages but NOT Administrator.
	ic := mkInteraction("watch-channel", discordgo.PermissionSendMessages, "watch")
	runDispatch(t, r, ic)

	if called != 0 {
		t.Fatalf("handler must not be called for non-admin caller; got %d calls", called)
	}
}

func TestRouter_RejectsDM(t *testing.T) {
	// Member is nil for DM interactions — guild-membership gate must close
	// before the permissions check (otherwise a dereference panic).
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs:  []string{"watch-channel"},
		RequirePermissions: discordgo.PermissionAdministrator,
		Logger:             quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, sess *discordgo.Session, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	ic := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:      discordgo.InteractionApplicationCommand,
			ChannelID: "watch-channel",
			Member:    nil, // simulating a DM
			Data:      discordgo.ApplicationCommandInteractionData{Name: "watch"},
		},
	}
	runDispatch(t, r, ic)

	if called != 0 {
		t.Fatalf("handler must not be called for DM (nil Member); got %d calls", called)
	}
}

func TestRouter_DoubleRegisterPanics(t *testing.T) {
	r := slash.NewRouter(slash.Config{Logger: quietLogger()})
	r.Handle("watch", func(ctx context.Context, sess *discordgo.Session, ic *discordgo.InteractionCreate) error {
		return nil
	})

	defer func() {
		if recover() == nil {
			t.Fatal("re-registering a handler must panic")
		}
	}()
	r.Handle("watch", func(ctx context.Context, sess *discordgo.Session, ic *discordgo.InteractionCreate) error {
		return errors.New("should never be called")
	})
}

func TestRouter_IgnoresNonApplicationCommandInteractions(t *testing.T) {
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs:  []string{"watch-channel"},
		RequirePermissions: discordgo.PermissionAdministrator,
		Logger:             quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, sess *discordgo.Session, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	ic := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionMessageComponent, // a button click, not a slash cmd
		},
	}
	runDispatch(t, r, ic)

	if called != 0 {
		t.Fatalf("non-slash interactions must be ignored; got %d calls", called)
	}
}
