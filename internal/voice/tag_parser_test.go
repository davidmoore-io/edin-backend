package voice_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/edin-space/edin-backend/internal/voice"
)

func TestParseTaggedText_SpeakOnly(t *testing.T) {
	segments := voice.ParseTaggedText("<speak>Well, found six markets nearby.</speak>")
	require.Len(t, segments, 1)
	assert.Equal(t, "speak", segments[0].Channel)
	assert.Equal(t, "Well, found six markets nearby.", segments[0].Content)
}

func TestParseTaggedText_DataOnly(t *testing.T) {
	segments := voice.ParseTaggedText("<data>| System | Price |\n|---|---|\n| Pytheas | 820K |</data>")
	require.Len(t, segments, 1)
	assert.Equal(t, "data", segments[0].Channel)
	assert.Contains(t, segments[0].Content, "Pytheas")
}

func TestParseTaggedText_MixedOrder(t *testing.T) {
	input := "<speak>Six markets found.</speak><data>| A | B |</data><speak>Best is Pytheas.</speak>"
	segments := voice.ParseTaggedText(input)
	require.Len(t, segments, 3)
	assert.Equal(t, "speak", segments[0].Channel)
	assert.Equal(t, "data", segments[1].Channel)
	assert.Equal(t, "speak", segments[2].Channel)
}

func TestParseTaggedText_UntaggedTextTreatedAsSpeak(t *testing.T) {
	segments := voice.ParseTaggedText("Hello Commander.")
	require.Len(t, segments, 1)
	assert.Equal(t, "speak", segments[0].Channel)
	assert.Equal(t, "Hello Commander.", segments[0].Content)
}

func TestParseTaggedText_EmptyString(t *testing.T) {
	segments := voice.ParseTaggedText("")
	assert.Empty(t, segments)
}

func TestParseTaggedText_SpeakContentHelper(t *testing.T) {
	input := "<speak>Found six options.</speak><data>| Col1 | Col2 |</data>"
	speak := voice.SpeakContent(voice.ParseTaggedText(input))
	assert.Equal(t, "Found six options.", speak)
}

func TestParseTaggedText_WhitespaceStripped(t *testing.T) {
	segments := voice.ParseTaggedText("<speak>  Hello.  </speak>")
	assert.Equal(t, "Hello.", segments[0].Content)
}

func TestStreamingTagParser_SpeakAcrossChunks(t *testing.T) {
	p := voice.NewStreamingTagParser()
	var speakChunks []string
	p.OnSpeakChunk(func(s string) { speakChunks = append(speakChunks, s) })

	p.Feed("<spe")
	p.Feed("ak>")
	p.Feed("Six markets")
	p.Feed(" found.</sp")
	p.Feed("eak>")

	require.Len(t, speakChunks, 1)
	assert.Equal(t, "Six markets found.", speakChunks[0])
}

func TestStreamingTagParser_DataChunk(t *testing.T) {
	p := voice.NewStreamingTagParser()
	var dataChunks []string
	p.OnDataChunk(func(s string) { dataChunks = append(dataChunks, s) })

	p.Feed("<data>| System | Price |</data>")

	require.Len(t, dataChunks, 1)
	assert.Contains(t, dataChunks[0], "System")
}

func TestStreamingTagParser_MixedChunks(t *testing.T) {
	p := voice.NewStreamingTagParser()
	var speak, data []string
	p.OnSpeakChunk(func(s string) { speak = append(speak, s) })
	p.OnDataChunk(func(s string) { data = append(data, s) })

	input := "<speak>Six markets found.</speak><data>| A |</data><speak>Best is Pytheas.</speak>"
	for _, ch := range input {
		p.Feed(string(ch))
	}

	require.Len(t, speak, 2)
	require.Len(t, data, 1)
	assert.Equal(t, "Six markets found.", speak[0])
	assert.Equal(t, "Best is Pytheas.", speak[1])
}

func TestStreamingTagParser_UntaggedTreatedAsSpeak(t *testing.T) {
	p := voice.NewStreamingTagParser()
	var speak []string
	p.OnSpeakChunk(func(s string) { speak = append(speak, s) })

	p.Feed("Hello Commander.")
	p.Flush()

	require.Len(t, speak, 1)
	assert.Equal(t, "Hello Commander.", speak[0])
}

func TestStreamingTagParser_FlushEmitsPartial(t *testing.T) {
	p := voice.NewStreamingTagParser()
	var speak []string
	p.OnSpeakChunk(func(s string) { speak = append(speak, s) })

	p.Feed("<speak>Hull at ninety-four percent.</speak>")
	p.Flush()

	require.Len(t, speak, 1)
	assert.Equal(t, "Hull at ninety-four percent.", speak[0])
}
