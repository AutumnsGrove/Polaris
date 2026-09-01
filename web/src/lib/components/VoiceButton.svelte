<script lang="ts">
	import { onDestroy } from 'svelte';
	import { Mic, Loader2 } from '@lucide/svelte';
	import { appState } from '$lib/state.svelte';

	// Voice input only ever populates the composer's draft text (and, for
	// the Whisper path, its running STT cost) — it never sends on its own.
	// Auto-sending on stop meant a misheard word went out before you could
	// see or fix it; routing through the same bindable draft the textarea
	// uses gives voice the same "review before send" behavior typing
	// already has.
	let {
		value = $bindable(''),
		sttCostUsd = $bindable<number | undefined>(undefined)
	}: { value: string; sttCostUsd?: number } = $props();

	let recording = $state(false);
	let transcribing = $state(false);
	let toggleMode = $derived(appState.settings.voiceInputMode === 'toggle');
	let mediaRecorder: MediaRecorder | null = null;
	let chunks: BlobPart[] = [];
	let stream: MediaStream | null = null;
	let pendingStop = false;
	let startedAt = 0;
	let recordedMimeType = 'audio/webm';

	// Text already in the composer when this recording started — new
	// transcript is appended after it rather than overwriting, so
	// dictating doesn't clobber anything you'd already typed.
	let baseText = '';

	// Finalized transcript pieces accumulated across this recording,
	// including across the onend/restart chain described below. Reset
	// only when a brand-new recording starts.
	let accumulatedFinal = '';

	function appendText(base: string, addition: string): string {
		if (!addition) return base;
		const trimmedBase = base.trimEnd();
		return trimmedBase ? `${trimmedBase} ${addition}` : addition;
	}

	// Minimal ambient typing for the non-standard, webkit-prefixed Web
	// Speech API — it isn't in TS's DOM lib, and only the handful of
	// members actually used below are declared.
	interface SpeechRecognitionResultLike {
		isFinal: boolean;
		0: { transcript: string };
	}
	interface SpeechRecognitionEventLike extends Event {
		results: ArrayLike<SpeechRecognitionResultLike>;
	}
	interface SpeechRecognitionLike extends EventTarget {
		continuous: boolean;
		interimResults: boolean;
		start(): void;
		stop(): void;
		abort(): void;
		onresult: ((e: SpeechRecognitionEventLike) => void) | null;
		onerror: ((e: Event & { error?: string }) => void) | null;
		onend: (() => void) | null;
	}

	let recognition: SpeechRecognitionLike | null = null;
	let usingLiveRecognition = false;
	// Flips permanently once the Web Speech API has actually failed once
	// this session — cheaper and more predictable than retrying a broken
	// API on every tap, and it degrades to the proven upload path instead.
	let speechRecognitionBroken = false;

	// iPadOS 13+ reports "Macintosh" in the UA string (desktop-site
	// spoofing) but is touch-capable, unlike a real Mac — maxTouchPoints
	// tells them apart. Deliberately Apple-only: Chrome/Edge on
	// desktop/Android also expose webkitSpeechRecognition, but backed by a
	// server-side engine and with none of this being the point (Whisper
	// already covers those platforms fine) — this path exists specifically
	// because Apple's on-device dictation is the one genuinely good,
	// streaming, zero-cost option, and it's iOS/iPadOS-only.
	function isIOS(): boolean {
		if (typeof navigator === 'undefined') return false;
		const ua = navigator.userAgent;
		return /iPad|iPhone|iPod/.test(ua) || (ua.includes('Macintosh') && navigator.maxTouchPoints > 1);
	}

	function getSpeechRecognitionCtor(): (new () => SpeechRecognitionLike) | null {
		if (typeof window === 'undefined') return null;
		const w = window as unknown as { webkitSpeechRecognition?: new () => SpeechRecognitionLike };
		return w.webkitSpeechRecognition ?? null;
	}

	// Computed lazily inside startRecording (browser-only), not at module
	// scope — MediaRecorder doesn't exist in Node, so a top-level reference
	// to it crashed SvelteKit's SSR render (`vite dev`/`vite preview`, and
	// the prerender step during `vite build`) with "MediaRecorder is not
	// defined" even though this component never actually runs during SSR
	// in practice.
	function pickMimeType(): string {
		return MediaRecorder.isTypeSupported('audio/webm;codecs=opus') ? 'audio/webm;codecs=opus' : 'audio/webm';
	}

	// On iOS/iPadOS, stream live partial transcripts straight from Safari's
	// on-device-backed Web Speech API into the composer as you talk — no
	// server round-trip, no Whisper cost.
	//
	// Deliberately NOT continuous: true. iOS Safari has a long-standing,
	// still-open WebKit bug where continuous mode either never fires a
	// result at all, or drops the final result specifically when .stop()
	// is called manually (vs. the engine timing out on its own) — which is
	// exactly "recorded audio, then did nothing with it." The standard
	// mobile-Safari workaround is to run short-lived recognition sessions
	// and re-`start()` a fresh one from onend for as long as the user is
	// still holding/toggled into recording, chaining them into what feels
	// like one continuous session. accumulatedFinal carries the finalized
	// text across that chain; each individual session's onresult only ever
	// covers its own (short) results list.
	function startSpeechRecognition() {
		const Ctor = getSpeechRecognitionCtor();
		if (!Ctor) throw new Error('SpeechRecognition unavailable');

		accumulatedFinal = '';
		usingLiveRecognition = true;
		beginRecognitionSession(Ctor);
	}

	function beginRecognitionSession(Ctor: new () => SpeechRecognitionLike) {
		recognition = new Ctor();
		recognition.continuous = false;
		recognition.interimResults = true;

		recognition.onresult = (e) => {
			let sessionFinal = '';
			let interimText = '';
			for (let i = 0; i < e.results.length; i++) {
				const result = e.results[i];
				if (result.isFinal) sessionFinal += result[0].transcript;
				else interimText += result[0].transcript;
			}
			if (sessionFinal) accumulatedFinal = appendText(accumulatedFinal, sessionFinal.trim());
			const combined = [accumulatedFinal, interimText].filter(Boolean).join(' ');
			value = appendText(baseText, combined);
		};

		recognition.onerror = (e) => {
			// 'no-speech' fires constantly in the restart chain below
			// (each short session times out on silence between phrases)
			// and 'aborted' fires when we call .stop()/.abort() ourselves
			// — neither means anything actually broke, so onend's normal
			// restart-or-finalize logic handles both without help here.
			if (e.error === 'no-speech' || e.error === 'aborted') return;
			console.error('speech recognition error', e.error);
			speechRecognitionBroken = true;
			usingLiveRecognition = false;
		};

		recognition.onend = () => {
			if (recording && usingLiveRecognition && !speechRecognitionBroken) {
				try {
					beginRecognitionSession(Ctor);
					return;
				} catch (err) {
					console.error('failed to restart speech recognition', err);
					speechRecognitionBroken = true;
				}
			}
			recording = false;
			usingLiveRecognition = false;
		};

		recognition.start();
	}

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
		baseText = value;

		if (isIOS() && !speechRecognitionBroken && getSpeechRecognitionCtor()) {
			try {
				startSpeechRecognition();
				if (pendingStop) stopRecording();
				return;
			} catch (err) {
				// Falls through to the upload path below for *this* attempt
				// too, not just future ones — no utterance has been lost yet
				// since recognition never actually started.
				console.error('speech recognition unavailable, falling back to upload', err);
				speechRecognitionBroken = true;
			}
		}

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
		recordedMimeType = pickMimeType();
		mediaRecorder = new MediaRecorder(stream, { mimeType: recordedMimeType });
		mediaRecorder.ondataavailable = (e) => {
			if (e.data.size > 0) chunks.push(e.data);
		};
		mediaRecorder.onstop = () => {
			stream?.getTracks().forEach((t) => t.stop());
			stream = null;
			const durationMs = Date.now() - startedAt;
			const blob = new Blob(chunks, { type: recordedMimeType });
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
		if (usingLiveRecognition) {
			recognition?.stop();
			recording = false;
			return;
		}
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
				if (data.text) {
					value = appendText(baseText, data.text);
					sttCostUsd = (sttCostUsd ?? 0) + (data.cost_usd ?? 0);
				}
			} else {
				console.error('transcription failed', await res.text());
			}
		} catch (err) {
			console.error('transcription request failed', err);
		} finally {
			transcribing = false;
		}
	}

	// Toggle mode's whole point is not needing to keep a finger/mouse down
	// for the entire memo (see settingVoiceInputMode's doc comment for
	// why hold-to-record went mostly unused) — one click starts it, the
	// next stops it, same as the iOS keyboard's own dictation button.
	function handleToggleClick() {
		if (recording) {
			stopRecording();
		} else {
			void startRecording();
		}
	}

	onDestroy(() => {
		stream?.getTracks().forEach((t) => t.stop());
		recognition?.abort();
	});
</script>

<button
	type="button"
	class="mic-btn"
	class:recording
	disabled={appState.busy || transcribing}
	onclick={toggleMode ? handleToggleClick : undefined}
	onmousedown={toggleMode ? undefined : startRecording}
	onmouseup={toggleMode ? undefined : stopRecording}
	onmouseleave={toggleMode ? undefined : stopRecording}
	ontouchstart={toggleMode
		? undefined
		: (e) => {
				e.preventDefault();
				void startRecording();
			}}
	ontouchend={toggleMode
		? undefined
		: (e) => {
				e.preventDefault();
				stopRecording();
			}}
	title={toggleMode
		? recording
			? 'Tap to stop recording'
			: 'Tap to record a voice memo'
		: 'Hold to record a voice memo'}
>
	{#if transcribing}
		<Loader2 size={16} class="spin" />
	{:else}
		<Mic size={16} />
	{/if}
</button>

<style>
	/* Ghost/borderless like the composer toolbar's other secondary
	   controls (see ComposerMenu's .plus-btn) — a plain outlined box read
	   as one more form-field competing with the textarea for attention.
	   Send stays the one solid, accent-colored control; everything else
	   in the toolbar recedes until it's actually doing something
	   (recording/transcribing). */
	.mic-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		border: 1px solid transparent;
		background: transparent;
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
		border-color: var(--color-border);
		background: var(--color-surface-2);
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
