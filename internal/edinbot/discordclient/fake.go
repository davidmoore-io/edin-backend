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
	PostErr        error
	EditErr        error
	ReplaceTextErr error
	DeleteErr      error

	mu               sync.Mutex
	postCalls        []FakePostCall
	editCalls        []FakeEditCall
	replaceTextCalls []FakeReplaceTextCall
	deleteCalls      []FakeDeleteCall
	nextID           atomic.Int64
}

type FakeReplaceTextCall struct {
	ChannelID string
	MessageID string
	Content   string
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

type FakeDeleteCall struct {
	ChannelID string
	MessageID string
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

func (f *FakeDiscordClient) ReplaceWithText(ctx context.Context, channelID, messageID, content string) error {
	if f.ReplaceTextErr != nil {
		return f.ReplaceTextErr
	}
	f.mu.Lock()
	f.replaceTextCalls = append(f.replaceTextCalls, FakeReplaceTextCall{ChannelID: channelID, MessageID: messageID, Content: content})
	f.mu.Unlock()
	return nil
}

func (f *FakeDiscordClient) ReplaceTextCalls() []FakeReplaceTextCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeReplaceTextCall, len(f.replaceTextCalls))
	copy(out, f.replaceTextCalls)
	return out
}

func (f *FakeDiscordClient) DeleteMessage(ctx context.Context, channelID, messageID string) error {
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	f.mu.Lock()
	f.deleteCalls = append(f.deleteCalls, FakeDeleteCall{ChannelID: channelID, MessageID: messageID})
	f.mu.Unlock()
	return nil
}

func (f *FakeDiscordClient) DeleteCalls() []FakeDeleteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeDeleteCall, len(f.deleteCalls))
	copy(out, f.deleteCalls)
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
	f.replaceTextCalls = nil
	f.deleteCalls = nil
	f.PostErr = nil
	f.EditErr = nil
	f.ReplaceTextErr = nil
	f.DeleteErr = nil
}
