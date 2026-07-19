<script lang="ts">
	import { onDestroy } from 'svelte';
	import { Mic, Loader2 } from '@lucide/svelte';
	import { appState } from '$lib/state.svelte';

	let recording = $state(false);
	let transcribing = $state(false);
	let mediaRecorder: MediaRecorder | null = null;
	let chunks: BlobPart[] = [];
	let stream: MediaStream | null = null;
	let pendingStop = false;
	let startedAt = 0;

	const mimeType = MediaRecorder.isTypeSupported('audio/webm;codecs=opus')
		? 'audio/webm;codecs=opus'
		: 'audio/webm';

	// A fresh getUserMedia call per recording — NOT cached. Holding a
	// stream open between recordings keeps the mic track live the whole
	// session, which browsers surface as "this tab is always recording"
	// in the tab/OS indicator. Once permission is granted the first time,
	// later calls resolve near-instantly (no dialog), so there's no real
	// cost to requesting fresh each time — and tracks are always stopped
	// the instant a recording ends (see onstop below).
	async function startRecording() {
		if (appState.busy || recording) return;
		recording = true;
		pendingStop = false;

		try {
			stream = await navigator.mediaDevices.getUserMedia({
				audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true }
			});
		} catch (err) {
			console.error('microphone access denied or unavailable', err);
			recording = false;
			return;
		}

		chunks = [];
		startedAt = Date.now();
		mediaRecorder = new MediaRecorder(stream, { mimeType });
		mediaRecorder.ondataavailable = (e) => {
			if (e.data.size > 0) chunks.push(e.data);
		};
		mediaRecorder.onstop = () => {
			stream?.getTracks().forEach((t) => t.stop());
			stream = null;
			const durationMs = Date.now() - startedAt;
			const blob = new Blob(chunks, { type: mimeType });
			void transcribeAndSend(blob, durationMs);
		};
		mediaRecorder.start();

		// The user already released the button while getUserMedia was
		// still resolving — stop right away instead of recording forever.
		// This is the actual fix for Whisper's "thank you" hallucination:
		// without this guard, releasing early during the permission
		// prompt meant the recording either never started or captured
		// near-silence, and Whisper hallucinates filler phrases on that.
		if (pendingStop) stopRecording();
	}

	function stopRecording() {
		if (!recording) return;
		if (!mediaRecorder || mediaRecorder.state !== 'recording') {
			pendingStop = true;
			return;
		}
		mediaRecorder.stop();
		recording = false;
	}

	async function transcribeAndSend(blob: Blob, durationMs: number) {
		// Anything under ~300ms is almost always an accidental tap, not a
		// memo — skip the round-trip rather than sending near-silent audio.
		if (durationMs < 300 || blob.size < 500) return;

		transcribing = true;
		try {
			const res = await fetch(`/api/transcribe?format=webm`, { method: 'POST', body: blob });
			if (res.ok) {
				const data = await res.json();
				if (data.text) appState.send(data.text, data.cost_usd);
			} else {
				console.error('transcription failed', await res.text());
			}
		} catch (err) {
			console.error('transcription request failed', err);
		} finally {
			transcribing = false;
		}
	}

	onDestroy(() => {
		stream?.getTracks().forEach((t) => t.stop());
	});
</script>

<button
	type="button"
	class="mic-btn"
	class:recording
	disabled={appState.busy || transcribing}
	onmousedown={startRecording}
	onmouseup={stopRecording}
	onmouseleave={stopRecording}
	ontouchstart={(e) => {
		e.preventDefault();
		void startRecording();
	}}
	ontouchend={(e) => {
		e.preventDefault();
		stopRecording();
	}}
	title="Hold to record a voice memo"
>
	{#if transcribing}
		<Loader2 size={16} class="spin" />
	{:else}
		<Mic size={16} />
	{/if}
</button>

<style>
	.mic-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		border: 1px solid var(--color-border);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		width: 38px;
		height: 38px;
		color: var(--color-text-dim);
		flex-shrink: 0;
		transition:
			border-color 0.18s var(--ease-out-expo),
			background-color 0.18s var(--ease-out-expo),
			color 0.18s var(--ease-out-expo),
			transform 0.18s var(--ease-out-expo),
			box-shadow 0.2s var(--ease-out-expo);
	}

	.mic-btn:hover:not(:disabled):not(.recording) {
		border-color: var(--color-border-strong);
		background: var(--color-surface-3);
		color: var(--color-text);
		transform: translateY(-1px);
	}

	.mic-btn:active:not(:disabled):not(.recording) {
		transform: translateY(0);
	}

	.mic-btn.recording {
		background: var(--color-danger);
		border-color: var(--color-danger);
		color: white;
		animation: mic-pulse 1.4s var(--ease-out-expo) infinite;
	}

	.mic-btn:disabled {
		opacity: 0.4;
		cursor: default;
	}

	@keyframes mic-pulse {
		0%, 100% {
			box-shadow: 0 0 0 0 color-mix(in srgb, var(--color-danger) 50%, transparent);
		}
		50% {
			box-shadow: 0 0 0 6px color-mix(in srgb, var(--color-danger) 0%, transparent);
		}
	}

	:global(.spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
