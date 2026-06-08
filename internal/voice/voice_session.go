package voice

import (
	"context"
	"sync"
	"time"
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
		// Tell EL we're done sending text. It will finish generating audio and
		// send isFinal: true. We must NOT cancel the read context yet — that
		// would kill ReadAudioChunks before the audio arrives.
		vs.client.Flush()          //nolint:errcheck
		vs.client.SendEndOfInput() //nolint:errcheck

		// Wait for ReadAudioChunks to complete naturally (isFinal: true received),
		// with a 10s ceiling so a stalled EL session can't block forever.
		done := make(chan struct{})
		go func() {
			vs.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			vs.cancelCtx() // force exit on timeout
			<-done
		}

		vs.client.Close() //nolint:errcheck
		close(vs.audioCh)
	})
}
