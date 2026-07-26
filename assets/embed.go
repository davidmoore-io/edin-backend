package assets

import (
	_ "embed"
	"encoding/base64"
)

//go:embed authentik/edin-logo.png
var edinLogoPNG []byte

// EDINLogoDataURI keeps small branded server-rendered pages self-contained.
var EDINLogoDataURI = "data:image/png;base64," +
	base64.StdEncoding.EncodeToString(edinLogoPNG)
