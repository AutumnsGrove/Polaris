// Text-to-speech via the backend's /api/speak endpoint (Kokoro-82M on
// OpenRouter, not the browser's built-in SpeechSynthesis — that defaults
// to a low-quality robotic voice on most systems).

let currentAudio: HTMLAudioElement | null = null;
let currentUrl: string | null = null;

/**
 * Synthesizes text and plays it back. Returns the USD cost reported by
 * the server (via the X-Tts-Cost-Usd header) so callers can fold it into
 * a running total — the raw-audio response has no JSON body to carry it.
 */
export async function speak(text: string, threadId?: string): Promise<number> {
	stopSpeaking();

	const res = await fetch('/api/speak', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ text, thread_id: threadId })
	});
	if (!res.ok) {
		console.error('TTS request failed', await res.text());
		return 0;
	}

	const costHeader = res.headers.get('X-Tts-Cost-Usd');
	const cost = costHeader ? parseFloat(costHeader) : 0;

	const blob = await res.blob();
	const url = URL.createObjectURL(blob);
	currentUrl = url;
	currentAudio = new Audio(url);
	currentAudio.onended = () => {
		if (currentUrl === url) {
			URL.revokeObjectURL(url);
			currentUrl = null;
		}
	};
	await currentAudio.play().catch((err) => console.error('audio playback failed', err));

	return cost;
}

export function stopSpeaking() {
	if (currentAudio) {
		currentAudio.pause();
		currentAudio = null;
	}
	if (currentUrl) {
		URL.revokeObjectURL(currentUrl);
		currentUrl = null;
	}
}
