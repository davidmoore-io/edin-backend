package voice_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/edin-space/edin-backend/internal/voice"
)

func mockELServer(t *testing.T, handler func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		handler(conn)
	}))
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestElevenLabsClient_SendText(t *testing.T) {
	var received []string
	srv := mockELServer(t, func(conn *websocket.Conn) {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil { return }
			received = append(received, string(msg))
		}
	})
	defer srv.Close()

	cfg := voice.ElevenLabsConfig{WSURL: wsURL(srv), VoiceID: "test_voice", ModelID: "eleven_flash_v2_5"}
	client := voice.NewElevenLabsClient(cfg)
	require.NoError(t, client.Connect(context.Background()))
	require.NoError(t, client.SendText("Well, found six markets.", false))
	time.Sleep(50 * time.Millisecond)

	require.NotEmpty(t, received)
	var frame map[string]any
	require.NoError(t, json.Unmarshal([]byte(received[len(received)-1]), &frame))
	assert.Equal(t, "Well, found six markets.", frame["text"])
}

func TestElevenLabsClient_ReceivesAudioChunks(t *testing.T) {
	fakeAudio := base64.StdEncoding.EncodeToString([]byte("MP3_BYTES"))
	srv := mockELServer(t, func(conn *websocket.Conn) {
		conn.ReadMessage() //nolint:errcheck
		conn.WriteJSON(map[string]any{"audio": fakeAudio, "isFinal": false}) //nolint:errcheck
		conn.WriteJSON(map[string]any{"audio": "", "isFinal": true})         //nolint:errcheck
	})
	defer srv.Close()

	cfg := voice.ElevenLabsConfig{WSURL: wsURL(srv), VoiceID: "v", ModelID: "m"}
	client := voice.NewElevenLabsClient(cfg)
	require.NoError(t, client.Connect(context.Background()))

	chunks := make(chan []byte, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go client.ReadAudioChunks(ctx, chunks) //nolint:errcheck
	time.Sleep(100 * time.Millisecond)

	require.NotEmpty(t, chunks)
	chunk := <-chunks
	assert.Equal(t, []byte("MP3_BYTES"), chunk)
}

func TestElevenLabsClient_FlushSendsFlushTrue(t *testing.T) {
	var received []map[string]any
	srv := mockELServer(t, func(conn *websocket.Conn) {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil { return }
			var m map[string]any
			json.Unmarshal(msg, &m) //nolint:errcheck
			received = append(received, m)
		}
	})
	defer srv.Close()

	cfg := voice.ElevenLabsConfig{WSURL: wsURL(srv), VoiceID: "v", ModelID: "m"}
	client := voice.NewElevenLabsClient(cfg)
	require.NoError(t, client.Connect(context.Background()))
	require.NoError(t, client.Flush())
	time.Sleep(50 * time.Millisecond)

	found := false
	for _, m := range received {
		if flush, ok := m["flush"].(bool); ok && flush {
			found = true
		}
	}
	assert.True(t, found)
}

func TestPersonaVoices_ForPersona(t *testing.T) {
	v := voice.PersonaVoices{
		TheMind: "mind_id", TheAnalyst: "analyst_id",
		BobUk: "bob_id", TheVeteran: "veteran_id",
	}
	assert.Equal(t, "analyst_id", v.ForPersona("the_analyst"))
	assert.Equal(t, "bob_id", v.ForPersona("bob_uk"))
	assert.Equal(t, "veteran_id", v.ForPersona("the_veteran"))
	assert.Equal(t, "mind_id", v.ForPersona("the_mind"))
	assert.Equal(t, "mind_id", v.ForPersona("unknown"))
}
