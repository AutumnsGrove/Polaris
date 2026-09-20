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
	// URL of the persisted read-aloud audio, set only on the done line and
	// only when messageId was given below (see gateway/voice_handlers.go's
	// speakStreamChunk.File doc comment).
	file?: string;
}

/**
 * Synthesizes text via /api/speak/stream (chunked sentence-by-sentence
 * server-side — see gateway/voice_handlers.go's handleSpeakStream — for
 * Kokoro synthesis latency, not for progressive client playback). Resolves
 * once the whole answer has finished synthesizing and been persisted, with
 * whatever cost was billed and the persisted file's URL. This used to also
 * invoke a per-chunk callback with a playable Audio element the instant
 * each chunk arrived, for lower time-to-first-audio on a long answer —
 * live testing found that queued chunk-by-chunk playback silently breaking
 * after the first chunk more often than not, and separately, Kokoro
 * synthesizing a typical answer end-to-end only takes a few seconds
 * anyway. Simpler and more robust to wait for the one real file and let
 * WaveformAudioPlayer own playback entirely, so per-chunk audio_base64
 * lines are received (the wire format still carries them, see
 * gateway/voice_handlers.go's speakStreamChunk) but intentionally ignored
 * here now.
 */
export async function synthesizeStream(
	text: string,
	threadId: string | undefined,
	messageId: number | undefined
): Promise<{ cost: number; error?: string; file?: string }> {
	const res = await fetch('/api/speak/stream', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ text, thread_id: threadId, message_id: messageId })
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
	let file: string | undefined;

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
				file = parsed.file;
				continue;
			}
			// Non-final lines' audio_base64/content_type are intentionally
			// unused now — see this function's doc comment.
		}
	}

	return { cost, error, file };
}
