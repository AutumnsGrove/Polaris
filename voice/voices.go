package voice

// Voice is one entry in the curated Kokoro-82M roster the Settings picker
// offers. ID is the exact string OpenRouter's /audio/speech endpoint takes as
// its "voice" field (Kokoro's own prefix scheme: first letter = accent, second
// = gender — see hexgrad/Kokoro-82M's VOICES.md).
type Voice struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Gender string `json:"gender"` // "female" or "male"
	Accent string `json:"accent"` // "american" or "british"
}

// Voices is deliberately a short list, not Kokoro's full roster — one of each
// gender/accent pairing is plenty for a single-operator tool (issue #114).
// Served to the frontend via GET /api/settings so the picker never hardcodes
// IDs that could drift from what the backend actually accepts. Order is the
// picker's display order (American f/m, then British f/m). Each ID also has
// a pre-rendered preview at web/static/voice-samples/<id>.mp3, made by
// dev/gen-voice-samples.sh — adding a voice here means re-running it.
var Voices = []Voice{
	{ID: "af_sky", Name: "Sky", Gender: "female", Accent: "american"},
	{ID: "am_onyx", Name: "Onyx", Gender: "male", Accent: "american"},
	{ID: "bf_lily", Name: "Lily", Gender: "female", Accent: "british"},
	{ID: "bm_daniel", Name: "Daniel", Gender: "male", Accent: "british"},
}

// IsValidVoice reports whether id is in the curated roster.
func IsValidVoice(id string) bool {
	for _, v := range Voices {
		if v.ID == id {
			return true
		}
	}
	return false
}
