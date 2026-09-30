import type { TimelineItem } from './types';

// Pure helpers for the Transponder call overlay (components/Transponder.svelte)
// — split out so they can be unit-tested without a DOM or real audio.

export type ThinkingChip = Extract<TimelineItem, { kind: 'tool' }> | Extract<TimelineItem, { kind: 'reasoning' }>;

// Condensed chip label for the Thinking screen — a shorter cousin of
// ToolEvent.svelte's label(), since this screen only ever shows a
// glanceable "what's it doing" list, not the full expandable detail
// the normal timeline gives each tool call. Includes reasoning bursts
// alongside tool calls — a model that's doing extended hidden thinking
// with no tool calls at all previously showed nothing but a static
// "Thinking…" the whole time, which read as stuck/broken rather than
// genuinely working.
export function chipLabel(item: ThinkingChip): string {
	if (item.kind === 'reasoning') return item.done ? 'Reasoned' : 'Reasoning…';
	if (item.tool === 'web_search') return `Searching: ${item.args?.query ?? ''}`;
	if (item.tool === 'web_read') return `Reading: ${item.args?.url ?? ''}`;
	if (item.tool === 'weather') return `Checking the weather`;
	if (item.tool === 'code_exec') return 'Running code';
	return item.tool;
}

// Peak amplitude per resolutionMs-wide bucket, normalized
// against this clip's own loudest bucket and exponent-exaggerated —
// mirrors WaveformAudioPlayer.svelte's decodePeaks() almost exactly
// (same reasoning: Kokoro's output has a narrow dynamic range, so
// stretching each clip's own peaks to fill 0..1 first reads far less
// flat than a fixed-constant normalization would).
export function computePeaks(buffer: AudioBuffer, resolutionMs: number): number[] {
	const channel = buffer.getChannelData(0);
	const bucketSize = Math.max(1, Math.floor((resolutionMs / 1000) * buffer.sampleRate));
	const raw: number[] = [];
	let maxPeak = 0;
	for (let i = 0; i < channel.length; i += bucketSize) {
		let peak = 0;
		const end = Math.min(i + bucketSize, channel.length);
		for (let j = i; j < end; j++) {
			const abs = Math.abs(channel[j]);
			if (abs > peak) peak = abs;
		}
		raw.push(peak);
		if (peak > maxPeak) maxPeak = peak;
	}
	return raw.map((peak) => {
		const normalized = maxPeak > 0 ? peak / maxPeak : 0;
		return Math.max(0.08, Math.pow(normalized, 2.2));
	});
}

export function pickMimeType(): string {
	return MediaRecorder.isTypeSupported('audio/webm;codecs=opus') ? 'audio/webm;codecs=opus' : 'audio/webm';
}

// voice_mode_instruction (prompts.yaml) already tells the model to
// avoid "reciting citations inline", but that's a request, not a
// guarantee — live-caught: a reply came back with real markdown link
// syntax ([Investor Relations](https://...)) sitting in turn.content,
// and .reply-card renders that content as plain text (see its own doc
// comment — deliberately not real markdown, unlike ChatView's), so the
// raw brackets/parens/URL show up on screen verbatim even though the
// audio itself (synthesized from the same string) never read it
// aloud. Strips just the [text](url) -> text shape rather than pulling
// in the full marked+DOMPurify pipeline ChatView uses — Transponder's
// reply card was never meant to render real markdown, only to avoid
// leaking its syntax when the model doesn't fully comply.
export function stripInlineMarkdownLinks(text: string): string {
	return text.replace(/\[([^\]]+)\]\((?:[^()\s]+)\)/g, '$1');
}

export function citationHost(url: string): string {
	try {
		return new URL(url).hostname.replace(/^www\./, '');
	} catch {
		return url;
	}
}

export function formatElapsed(sec: number): string {
	const m = Math.floor(sec / 60);
	const s = sec % 60;
	return `${m}:${s.toString().padStart(2, '0')}`;
}
