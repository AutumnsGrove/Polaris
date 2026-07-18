<script lang="ts">
	import { Mic, Loader2 } from '@lucide/svelte';
	import { appState } from '$lib/state.svelte';

	let recording = $state(false);
	let transcribing = $state(false);
	let mediaRecorder: MediaRecorder | null = null;
	let chunks: BlobPart[] = [];
	let stream: MediaStream | null = null;

	async function startRecording() {
		if (appState.busy || recording) return;
		try {
			stream = await navigator.mediaDevices.getUserMedia({ audio: true });
		} catch (err) {
			console.error('microphone access denied or unavailable', err);
			return;
		}
		chunks = [];
		mediaRecorder = new MediaRecorder(stream, { mimeType: 'audio/webm' });
		mediaRecorder.ondataavailable = (e) => {
			if (e.data.size > 0) chunks.push(e.data);
		};
		mediaRecorder.onstop = () => {
			stream?.getTracks().forEach((t) => t.stop());
			stream = null;
			const blob = new Blob(chunks, { type: 'audio/webm' });
			void transcribeAndSend(blob);
		};
		mediaRecorder.start();
		recording = true;
	}

	function stopRecording() {
		if (mediaRecorder && recording) {
			mediaRecorder.stop();
			recording = false;
		}
	}

	async function transcribeAndSend(blob: Blob) {
		// Recordings under ~300ms are almost always an accidental tap, not
		// a memo — skip the round-trip rather than sending empty audio.
		if (blob.size < 1000) return;

		transcribing = true;
		try {
			const res = await fetch('/api/transcribe?format=webm', { method: 'POST', body: blob });
			if (res.ok) {
				const data = await res.json();
				if (data.text) appState.send(data.text);
			} else {
				console.error('transcription failed', await res.text());
			}
		} catch (err) {
			console.error('transcription request failed', err);
		} finally {
			transcribing = false;
		}
	}
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
