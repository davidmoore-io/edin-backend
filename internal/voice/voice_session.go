package voice

import (
	"context"
	"sync"
)

// VoiceSession manages TTS for one chat turn.
type VoiceSession struct {
	client    *ElevenLabsClient
	audioCh   chan []byte
	cancelCtx context.CancelFunc
	wg        sync.WaitGroup
	once      sync.Once
}

func NewVoiceSession(ctx context.Context, cfg ElevenLabsConfig, audioCh chan []byte) (*VoiceSession, error) {
	client := NewElevenLabsClient(cfg)
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	readCtx, cancel := context.WithCancel(ctx)
	vs := &VoiceSession{client: client, audioCh: audioCh, cancelCtx: cancel}
	vs.wg.Add(1)
	go func() {
		defer vs.wg.Done()
		client.ReadAudioChunks(readCtx, audioCh) //nolint:errcheck
	}()
	return vs, nil
}

func (vs *VoiceSession) SendSpeakContent(text string) error {
	if text == "" {
		return nil
	}
	return vs.client.SendText(text, true)
}

func (vs *VoiceSession) Dispose() {
	vs.once.Do(func() {
		vs.cancelCtx()
		vs.client.Flush()  //nolint:errcheck
		vs.client.Close()  //nolint:errcheck
		vs.wg.Wait()
		close(vs.audioCh)
	})
}
