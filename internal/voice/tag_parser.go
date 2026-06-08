package voice

import (
	"strings"
)

// Segment is one piece of EDIN's response with its output channel.
type Segment struct {
	Channel string // "speak" or "data"
	Content string
}

// ParseTaggedText parses a complete EDIN reply. Text outside tags = speak.
func ParseTaggedText(text string) []Segment {
	var segments []Segment
	remaining := text

	for len(remaining) > 0 {
		speakStart := strings.Index(remaining, "<speak>")
		dataStart := strings.Index(remaining, "<data>")

		if speakStart == -1 && dataStart == -1 {
			if content := strings.TrimSpace(remaining); content != "" {
				segments = append(segments, Segment{Channel: "speak", Content: content})
			}
			break
		}

		var tagStart int
		var channel, openTag, closeTag string
		if speakStart != -1 && (dataStart == -1 || speakStart < dataStart) {
			tagStart, channel, openTag, closeTag = speakStart, "speak", "<speak>", "</speak>"
		} else {
			tagStart, channel, openTag, closeTag = dataStart, "data", "<data>", "</data>"
		}

		if pre := strings.TrimSpace(remaining[:tagStart]); pre != "" {
			segments = append(segments, Segment{Channel: "speak", Content: pre})
		}

		inner := remaining[tagStart+len(openTag):]
		end := strings.Index(inner, closeTag)
		if end == -1 {
			if content := strings.TrimSpace(inner); content != "" {
				segments = append(segments, Segment{Channel: channel, Content: content})
			}
			break
		}

		if content := strings.TrimSpace(inner[:end]); content != "" {
			segments = append(segments, Segment{Channel: channel, Content: content})
		}
		remaining = inner[end+len(closeTag):]
	}

	return segments
}

// SpeakContent concatenates the content of all speak segments.
func SpeakContent(segments []Segment) string {
	var b strings.Builder
	for _, s := range segments {
		if s.Channel == "speak" {
			if b.Len() > 0 {
				b.WriteString(" ")
			}
			b.WriteString(s.Content)
		}
	}
	return b.String()
}

// StreamingTagParser handles text arriving in arbitrary-size chunks.
// Callbacks fire as soon as a complete tagged segment is received.
type StreamingTagParser struct {
	buf          strings.Builder
	onSpeakChunk func(string)
	onDataChunk  func(string)
}

func NewStreamingTagParser() *StreamingTagParser {
	return &StreamingTagParser{
		onSpeakChunk: func(string) {},
		onDataChunk:  func(string) {},
	}
}

func (p *StreamingTagParser) OnSpeakChunk(fn func(string)) { p.onSpeakChunk = fn }
func (p *StreamingTagParser) OnDataChunk(fn func(string))  { p.onDataChunk = fn }

func (p *StreamingTagParser) Feed(chunk string) {
	p.buf.WriteString(chunk)
	p.scan()
}

func (p *StreamingTagParser) Flush() {
	if rest := strings.TrimSpace(p.buf.String()); rest != "" {
		if !strings.Contains(rest, "<") {
			p.onSpeakChunk(rest)
			p.buf.Reset()
		}
	}
}

func (p *StreamingTagParser) scan() {
	for {
		text := p.buf.String()
		speakOpen := strings.Index(text, "<speak>")
		dataOpen := strings.Index(text, "<data>")

		if speakOpen == -1 && dataOpen == -1 {
			if end := strings.Index(text, "</speak>"); end != -1 {
				if content := strings.TrimSpace(text[:end]); content != "" {
					p.onSpeakChunk(content)
				}
				p.buf.Reset()
				p.buf.WriteString(text[end+len("</speak>"):])
				continue
			}
			if end := strings.Index(text, "</data>"); end != -1 {
				if content := strings.TrimSpace(text[:end]); content != "" {
					p.onDataChunk(content)
				}
				p.buf.Reset()
				p.buf.WriteString(text[end+len("</data>"):])
				continue
			}
			return
		}

		var tagStart int
		var openTag, closeTag string
		var callback func(string)

		if speakOpen != -1 && (dataOpen == -1 || speakOpen < dataOpen) {
			tagStart, openTag, closeTag, callback = speakOpen, "<speak>", "</speak>", p.onSpeakChunk
		} else {
			tagStart, openTag, closeTag, callback = dataOpen, "<data>", "</data>", p.onDataChunk
		}

		if pre := strings.TrimSpace(text[:tagStart]); pre != "" && !strings.Contains(pre, "<") {
			p.onSpeakChunk(pre)
		}

		inner := text[tagStart+len(openTag):]
		end := strings.Index(inner, closeTag)
		if end == -1 {
			if tagStart > 0 {
				p.buf.Reset()
				p.buf.WriteString(text[tagStart:])
			}
			return
		}

		if content := strings.TrimSpace(inner[:end]); content != "" {
			callback(content)
		}
		remaining := inner[end+len(closeTag):]
		p.buf.Reset()
		p.buf.WriteString(remaining)
	}
}
