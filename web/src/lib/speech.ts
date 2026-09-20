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

// One decoded, independently-playable chunk — each speakStreamChunk's
// audio_base64 is a complete, standalone WAV (gateway wraps each chunk's
// raw PCM individually via voice.WrapPCMAsWAV before base64-encoding it),
// not a fragment of one continuous stream, so handing these off to
// sequential <audio>.play() calls needs no gapless-PCM-stitching trickery.
export interface SpeechChunk {
	audioBase64: string;
	contentType: string;
}

/**
 * Synthesizes text via /api/speak/stream (chunked sentence-by-sentence
 * server-side — see gateway/voice_handlers.go's handleSpeakStream). Always
 * resolves once the whole answer has finished synthesizing and been
 * persisted, with whatever cost was billed and the persisted file's URL —
 * that contract is unchanged, so every existing caller (AudioPlayer.
 * readAloud, WaveformAudioPlayer's normal-chat flow) behaves exactly as
 * before. The optional onChunk callback is new: when given, it fires as
 * each chunk's audio actually arrives, for a caller that wants to start
 * playing before the full answer is done synthesizing.
 *
 * This capability existed once before and was removed — "queued
 * chunk-by-chunk playback silently breaking after the first chunk more
 * often than not" — for the *normal chat* read-aloud flow specifically,
 * where a long multi-paragraph answer could mean many chunks and a
 * scrubbable player has real correctness expectations. Transponder's
 * voice_mode answers are short (1-3 sentences = 1-3 chunks per
 * voice_mode_instruction), and there's no scrubber to keep correct — a
 * much smaller, lower-risk surface to revive this for. Left disabled
 * (onChunk omitted) for every other caller, so this doesn't reopen that
 * old bug for the path it was actually found on.
 */
export async function synthesizeStream(
	text: string,
	threadId: string | undefined,
	messageId: number | undefined,
	onChunk?: (chunk: SpeechChunk) => void
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
			if (onChunk && parsed.audio_base64 && parsed.content_type) {
				onChunk({ audioBase64: parsed.audio_base64, contentType: parsed.content_type });
			}
		}
	}

	return { cost, error, file };
}
