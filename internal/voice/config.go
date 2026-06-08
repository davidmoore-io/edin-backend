package voice

import "os"

// PersonaVoices maps EDIN persona keys to ElevenLabs voice IDs.
type PersonaVoices struct {
	TheMind    string
	TheAnalyst string
	BobUk      string
	TheVeteran string
}

// LoadPersonaVoices reads voice IDs from env, with EDIN defaults.
// Voice IDs confirmed 2026-06-08 via API.
func LoadPersonaVoices() PersonaVoices {
	return PersonaVoices{
		TheMind:    envOrDefault("ELEVENLABS_VOICE_THE_MIND", "tRXjpvqiDnZPEA7B2Izf"),
		TheAnalyst: envOrDefault("ELEVENLABS_VOICE_THE_ANALYST", "Oa7ZtEdaNoSb1hXFIpZE"),
		BobUk:      envOrDefault("ELEVENLABS_VOICE_BOB_UK", "LjZBisX2xAdJtahnQeBB"),
		TheVeteran: envOrDefault("ELEVENLABS_VOICE_THE_VETERAN", "SOWvz3ASTONJOosMtJsO"),
	}
}

// ForPersona returns the voice ID for the given persona API key.
func (v PersonaVoices) ForPersona(persona string) string {
	switch persona {
	case "the_analyst":
		return v.TheAnalyst
	case "bob_uk":
		return v.BobUk
	case "the_veteran":
		return v.TheVeteran
	default:
		return v.TheMind
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
