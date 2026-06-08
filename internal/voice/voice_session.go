package voice

import (
	"context"
	"log"
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
		if err := client.ReadAudioChunks(readCtx, audioCh); err != nil {
			log.Printf("[voice] ReadAudioChunks exited: %v", err)
		} else {
			log.Printf("[voice] ReadAudioChunks completed (isFinal received)")
		}
	}()
	return vs, nil
}

func (vs *VoiceSession) Dispose() {
	vs.once.Do(func() {
		// Signal EL: done sending text, please finish generating.
		vs.client.Flush()          //nolint:errcheck
		vs.client.SendEndOfInput() //nolint:errcheck

		// Wait up to 10s for EL to send all audio and isFinal: true.
		done := make(chan struct{})
		go func() {
			vs.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			log.Printf("[voice] Dispose: ReadAudioChunks completed naturally")
		case <-time.After(10 * time.Second):
			log.Printf("[voice] Dispose: timed out waiting for EL isFinal")
		}

		// Always cancel context and close WS to unblock ReadMessage() if still
		// running (covers both timeout path and ensuring clean shutdown).
		vs.cancelCtx()
		vs.client.Close() //nolint:errcheck
		vs.wg.Wait()      // wait for goroutine to actually exit after WS close
		close(vs.audioCh)
	})
}

func (vs *VoiceSession) SendSpeakContent(text string) error {
	if text == "" {
		return nil
	}
	log.Printf("[voice] SendSpeakContent: %d chars → ElevenLabs", len(text))
	return vs.client.SendText(text, true)
}
