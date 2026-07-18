// Text-to-speech via the backend's /api/speak endpoint (Kokoro-82M on
// OpenRouter, not the browser's built-in SpeechSynthesis — that defaults
// to a low-quality robotic voice on most systems).
//
// This module only fetches and constructs the Audio element — play/pause/
// stop and "is this currently playing" state live in state.svelte.ts,
// since that's what needs to drive the read-aloud button's icon.

export interface SpeechResult {
	audio: HTMLAudioElement;
	cost: number;
}

/**
 * Synthesizes text and returns a ready-to-play Audio element plus the
 * USD cost reported by the server (via the X-Tts-Cost-Usd header — the
 * raw-audio response has no JSON body to carry it). Does not play it;
 * the caller controls playback so it can track start/stop state.
 */
export async function synthesize(text: string, threadId?: string): Promise<SpeechResult | null> {
	const res = await fetch('/api/speak', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ text, thread_id: threadId })
	});
	if (!res.ok) {
		console.error('TTS request failed', await res.text());
		return null;
	}

	const costHeader = res.headers.get('X-Tts-Cost-Usd');
	const cost = costHeader ? parseFloat(costHeader) : 0;

	const blob = await res.blob();
	const url = URL.createObjectURL(blob);
	const audio = new Audio(url);
	audio.addEventListener('ended', () => URL.revokeObjectURL(url), { once: true });

	return { audio, cost };
}
