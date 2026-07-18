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

	// Cached and reused across recordings: getUserMedia's permission
	// prompt is async, and if the user starts speaking while it's still
	// showing, the recording hasn't actually started yet — the classic
	// cause of Whisper's "thank you" hallucination on near-silent audio.
	// Requesting once and reusing the stream means that race can only
	// ever happen on the very first recording, not every time.
	async function ensureStream(): Promise<MediaStream> {
		if (!stream) {
			stream = await navigator.mediaDevices.getUserMedia({
				audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true }
			});
		}
		return stream;
	}

	async function startRecording() {
		if (appState.busy || recording) return;
		recording = true;
		pendingStop = false;

		let activeStream: MediaStream;
		try {
			activeStream = await ensureStream();
		} catch (err) {
			console.error('microphone access denied or unavailable', err);
			recording = false;
			return;
		}

		chunks = [];
		startedAt = Date.now();
		mediaRecorder = new MediaRecorder(activeStream, { mimeType });
		mediaRecorder.ondataavailable = (e) => {
			if (e.data.size > 0) chunks.push(e.data);
		};
		mediaRecorder.onstop = () => {
			const durationMs = Date.now() - startedAt;
			const blob = new Blob(chunks, { type: mimeType });
			void transcribeAndSend(blob, durationMs);
		};
		mediaRecorder.start();

		// The user already released the button while getUserMedia was
		// still resolving — stop right away instead of recording forever.
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
	}

	.mic-btn.recording {
		background: var(--color-danger);
		border-color: var(--color-danger);
		color: white;
	}

	.mic-btn:disabled {
		opacity: 0.4;
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
