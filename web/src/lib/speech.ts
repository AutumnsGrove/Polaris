// Text-to-speech via the browser's built-in Web Speech API — zero extra
// RAM on the potato, no server round-trip, works offline once the page
// is loaded. Deliberately not using a server-side TTS engine (Piper,
// etc.) here: this is a stateless web app, not a persistent process
// where spawning a TTS sidecar would make sense.

function stripMarkdownForSpeech(md: string): string {
	return md
		.replace(/\[([^\]]+)\]\([^)]+\)/g, '$1') // [text](url) -> text
		.replace(/[*_#`]/g, '')
		.replace(/^\s*[-•]\s+/gm, '')
		.replace(/\n{2,}/g, '. ')
		.trim();
}

export function speak(text: string) {
	if (typeof window === 'undefined' || !('speechSynthesis' in window)) return;
	const cleaned = stripMarkdownForSpeech(text);
	if (!cleaned) return;

	// Cancel anything already playing — a new answer shouldn't queue
	// behind a stale one.
	window.speechSynthesis.cancel();
	const utterance = new SpeechSynthesisUtterance(cleaned);
	window.speechSynthesis.speak(utterance);
}

export function stopSpeaking() {
	if (typeof window !== 'undefined' && 'speechSynthesis' in window) {
		window.speechSynthesis.cancel();
	}
}
