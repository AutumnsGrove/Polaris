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

// One line of /api/speak/stream's NDJSON response — see gateway's
// speakStreamChunk. Either a synthesized chunk, a fatal error partway
// through, or the final summary line.
interface SpeakStreamLine {
	seq: number;
	audio_base64?: string;
	content_type?: string;
	error?: string;
	done?: boolean;
	cost_usd?: number;
}

function base64ToBlob(base64: string, contentType: string): Blob {
	const binary = atob(base64);
	const bytes = new Uint8Array(binary.length);
	for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
	return new Blob([bytes], { type: contentType });
}

/**
 * Synthesizes text in sentence-sized chunks via /api/speak/stream,
 * invoking onChunk with a ready-to-play Audio element for each chunk the
 * instant it arrives — the caller can start playback of the first chunk
 * long before the rest of a multi-paragraph answer has finished
 * synthesizing. Resolves once the stream ends (all chunks delivered, or a
 * fatal error partway through) with whatever cost was actually billed.
 */
export async function synthesizeStream(
	text: string,
	threadId: string | undefined,
	onChunk: (audio: HTMLAudioElement) => void
): Promise<{ cost: number; error?: string }> {
	const res = await fetch('/api/speak/stream', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ text, thread_id: threadId })
	});
	if (!res.ok || !res.body) {
		console.error('TTS stream request failed', res.ok ? 'no response body' : await res.text());
		return { cost: 0, error: 'request failed' };
	}

	const reader = res.body.getReader();
	const decoder = new TextDecoder();
	let buffered = '';
	let cost = 0;
	let error: string | undefined;

	// NDJSON: each line is a complete JSON value, but a single chunk read
	// from the stream can split a line across two reads (or contain
	// several) — buffer and only parse once a full "\n"-terminated line
	// has arrived, same shape as any line-delimited streaming protocol.
	while (true) {
		const { done, value } = await reader.read();
		if (done) break;
		buffered += decoder.decode(value, { stream: true });

		let newlineIndex: number;
		while ((newlineIndex = buffered.indexOf('\n')) !== -1) {
			const line = buffered.slice(0, newlineIndex).trim();
			buffered = buffered.slice(newlineIndex + 1);
			if (!line) continue;

			const parsed: SpeakStreamLine = JSON.parse(line);
			if (parsed.error) {
				error = parsed.error;
				continue;
			}
			if (parsed.done) {
				cost = parsed.cost_usd ?? cost;
				continue;
			}
			if (parsed.audio_base64 && parsed.content_type) {
				const blob = base64ToBlob(parsed.audio_base64, parsed.content_type);
				const url = URL.createObjectURL(blob);
				const audio = new Audio(url);
				audio.addEventListener('ended', () => URL.revokeObjectURL(url), { once: true });
				onChunk(audio);
			}
		}
	}

	return { cost, error };
}
