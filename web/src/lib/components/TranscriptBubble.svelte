<script lang="ts">
	// What the STT model actually heard, shown on Transponder's Thinking and
	// Speaking screens so a bad transcription is visible immediately, in the
	// call itself.
	let { text }: { text: string } = $props();
</script>

<div class="transcript-bubble">
	<span class="transcript-label">You said</span>
	{text}
</div>

<style>
	/* What the STT model actually heard — deliberately smaller/dimmer than
	   .reply-card so it reads as "for reference" rather than competing
	   with the assistant's own answer, but still fully legible: the whole
	   point is catching a bad transcription immediately, in the call
	   itself, not after leaving Transponder to read the thread. */
	.transcript-bubble {
		width: 100%;
		max-width: 300px;
		/* A long push-to-talk hold can transcribe to several sentences —
		   capped and internally scrollable so it can't push the orb/reply/
		   footer around or blow out the fixed-height call screen. */
		max-height: 96px;
		overflow-y: auto;
		background: color-mix(in srgb, var(--color-surface-2) 60%, transparent);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 13px;
		line-height: 1.45;
		color: var(--color-text-dim);
	}

	.transcript-label {
		display: block;
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-accent);
		margin-bottom: 2px;
	}
</style>
