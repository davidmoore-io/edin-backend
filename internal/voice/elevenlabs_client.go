package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// ElevenLabsConfig configures the streaming TTS client.
type ElevenLabsConfig struct {
	WSURL   string // override for testing
	APIKey  string
	VoiceID string
	ModelID string // "eleven_flash_v2_5"
	Format  string // "mp3_44100_128" — NOT raw PCM
}

func (c ElevenLabsConfig) wsEndpoint() string {
	if c.WSURL != "" {
		return c.WSURL
	}
	return fmt.Sprintf(
		"wss://api.elevenlabs.io/v1/text-to-speech/%s/stream-input?xi_api_key=%s&model_id=%s&output_format=%s",
		c.VoiceID, c.APIKey, c.ModelID, c.Format,
	)
}

// ElevenLabsClient manages a single streaming TTS WebSocket session.
type ElevenLabsClient struct {
	cfg  ElevenLabsConfig
	conn *websocket.Conn
	mu   sync.Mutex
}

func NewElevenLabsClient(cfg ElevenLabsConfig) *ElevenLabsClient {
	if cfg.Format == "" {
		cfg.Format = "mp3_44100_128"
	}
	if cfg.ModelID == "" {
		cfg.ModelID = "eleven_flash_v2_5"
	}
	return &ElevenLabsClient{cfg: cfg}
}

func (c *ElevenLabsClient) Connect(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.cfg.wsEndpoint(), http.Header{})
	if err != nil {
		return fmt.Errorf("elevenlabs dial: %w", err)
	}
	c.conn = conn
	return c.send(map[string]any{
		"text": " ",
		"voice_settings": map[string]any{
			"stability": 0.5, "similarity_boost": 0.75,
		},
		"generation_config": map[string]any{
			"chunk_length_schedule": []int{50, 100, 150},
		},
	})
}

func (c *ElevenLabsClient) SendText(text string, flush bool) error {
	msg := map[string]any{"text": text}
	if flush {
		msg["flush"] = true
	}
	return c.send(msg)
}

func (c *ElevenLabsClient) Flush() error {
	return c.send(map[string]any{"text": " ", "flush": true})
}

func (c *ElevenLabsClient) ReadAudioChunks(ctx context.Context, out chan<- []byte) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return err
		}
		var frame struct {
			Audio   string `json:"audio"`
			IsFinal bool   `json:"isFinal"`
		}
		if err := json.Unmarshal(msg, &frame); err != nil {
			continue
		}
		if frame.Audio != "" {
			decoded, err := base64.StdEncoding.DecodeString(frame.Audio)
			if err == nil && len(decoded) > 0 {
				select {
				case out <- decoded:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		if frame.IsFinal {
			return nil
		}
	}
}

func (c *ElevenLabsClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	// Send EOS frame without calling send() to avoid re-locking mu.
	_ = c.conn.WriteJSON(map[string]any{"text": ""})
	return c.conn.Close()
}

func (c *ElevenLabsClient) send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(v)
}
