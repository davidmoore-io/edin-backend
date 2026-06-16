package httpapi

import "testing"

func TestSplitImageDataURI(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantB64   string
		wantMedia string
	}{
		{
			name:      "png data uri (client format)",
			in:        "data:image/png;base64,aGVsbG8=",
			wantB64:   "aGVsbG8=",
			wantMedia: "image/png",
		},
		{
			name:      "jpeg data uri",
			in:        "data:image/jpeg;base64,/9j/4AAQ",
			wantB64:   "/9j/4AAQ",
			wantMedia: "image/jpeg",
		},
		{
			name:      "raw base64 without prefix defaults to png",
			in:        "aGVsbG8=",
			wantB64:   "aGVsbG8=",
			wantMedia: "image/png",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b64, media := splitImageDataURI(c.in)
			if b64 != c.wantB64 {
				t.Fatalf("b64 = %q, want %q", b64, c.wantB64)
			}
			if media != c.wantMedia {
				t.Fatalf("media = %q, want %q", media, c.wantMedia)
			}
		})
	}
}
