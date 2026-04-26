package discordclient

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/bwmarrin/discordgo"
)

// FakeDiscordClient records every call instead of hitting the network. Used by
// publisher_test, scheduler_test, and integration_test. Tests can inject errors
// via PostErr / EditErr.
type FakeDiscordClient struct {
	PostErr error
	EditErr error

	mu        sync.Mutex
	postCalls []FakePostCall
	editCalls []FakeEditCall
	nextID    atomic.Int64
}

type FakePostCall struct {
	ChannelID string
	Embed     *discordgo.MessageEmbed
}

type FakeEditCall struct {
	ChannelID string
	MessageID string
	Embed     *discordgo.MessageEmbed
}

func NewFakeDiscordClient() *FakeDiscordClient {
	return &FakeDiscordClient{}
}

func (f *FakeDiscordClient) PostMessage(ctx context.Context, channelID string, embed *discordgo.MessageEmbed) (string, error) {
	if f.PostErr != nil {
		return "", f.PostErr
	}
	id := strconv.FormatInt(f.nextID.Add(1), 10)
	f.mu.Lock()
	f.postCalls = append(f.postCalls, FakePostCall{ChannelID: channelID, Embed: embed})
	f.mu.Unlock()
	return "fake-msg-" + id, nil
}

func (f *FakeDiscordClient) EditMessage(ctx context.Context, channelID, messageID string, embed *discordgo.MessageEmbed) error {
	if f.EditErr != nil {
		return f.EditErr
	}
	f.mu.Lock()
	f.editCalls = append(f.editCalls, FakeEditCall{ChannelID: channelID, MessageID: messageID, Embed: embed})
	f.mu.Unlock()
	return nil
}

func (f *FakeDiscordClient) PostCalls() []FakePostCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakePostCall, len(f.postCalls))
	copy(out, f.postCalls)
	return out
}

func (f *FakeDiscordClient) EditCalls() []FakeEditCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeEditCall, len(f.editCalls))
	copy(out, f.editCalls)
	return out
}

// Reset clears recorded calls and injected errors. Useful between phases of
// a multi-step test.
func (f *FakeDiscordClient) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postCalls = nil
	f.editCalls = nil
	f.PostErr = nil
	f.EditErr = nil
}
