<script lang="ts">
	import { onDestroy } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import { Mic, PhoneOff, X, Loader2, Volume2 } from '@lucide/svelte';
	import type { TimelineItem } from '$lib/types';

	// Full-screen push-to-talk call UI onto the current thread — see
	// docs/plans/transponder.md. Not a parallel thread type: it sends over
	// the exact same appState.send()/WebSocket path as the normal composer
	// (with voiceMode: true), so dismissing this overlay drops straight
	// back into the ordinary ChatView with every turn from the call already
	// visible there, same as if it had been typed.
	let { onClose }: { onClose: () => void } = $props();

	type Phase = 'idle' | 'listening' | 'thinking' | 'speaking';
	let phase = $state<Phase>('idle');
	let transcribing = $state(false);
	// What Whisper/whichever STT model actually heard, shown on the
	// Thinking and Speaking screens so a bad transcription is visible
	// immediately, in the call itself — not something only discoverable
	// afterward by leaving Transponder and reading the thread.
	let lastTranscript = $state('');
	let elapsedSec = $state(0);
	let elapsedTimer: ReturnType<typeof setInterval> | undefined;

	let toggleMode = $derived(appState.settings.voiceInputMode === 'toggle');
	let mediaRecorder: MediaRecorder | null = null;
	let chunks: BlobPart[] = [];
	let micStream: MediaStream | null = null;
	let recordingStartedAt = 0;
	let recordedMimeType = 'audio/webm';
	// Mirrors VoiceButton.svelte's own pendingStop — the operator released
	// the mic while getUserMedia() was still resolving (a real gap on a
	// device where permission isn't pre-granted, and non-zero even when it
	// is), so there's no mediaRecorder yet for stopRecording() to act on.
	// Without this, that release was silently dropped and the recording
	// just kept running until something else stopped it.
	let pendingStop = false;

	// The assistant turn index this call's current round is waiting on —
	// appState.turns is the same global array ChatView renders, so a round
	// started here shows up there too if the operator backs out mid-turn.
	let assistantIndex = $state<number | null>(null);
	let speakingTriggered = false;
	let turn = $derived(assistantIndex !== null ? appState.turns[assistantIndex] : undefined);
	let audioEl: HTMLAudioElement | undefined = $state();
	// Real playback state, driven only by the <audio> element's own
	// 'playing'/'pause'/'ended' events — see handlePlaybackReady's doc
	// comment for why this replaced a play()-promise-based check. The
	// file itself checks out fine server-side (verified directly against
	// a real persisted .wav: ~85% non-zero PCM payload, valid header), so
	// a failure here is a browser/playback-state problem, not a broken
	// file.
	let isPlaying = $state(false);

	// --- Web Audio: mic-input analysis only, not playback. Real frequency
	// data drove the Listening AND Speaking orb in the original plan (see
	// docs/plans/transponder.md's "Resolved" section) — but routing the
	// reply's own <audio> element through a MediaElementAudioSourceNode for
	// that turned out to be the actual bug behind "text shows, nothing
	// plays" (confirmed live on desktop Firefox, so this isn't just an iOS
	// Safari thing): once an element is routed that way, ITS OUTPUT ONLY
	// REACHES SPEAKERS VIA THE GRAPH — and browsers suspend an idle
	// AudioContext during the multi-second Thinking gap, then refuse to
	// resume it later since that resume isn't happening inside a real tap
	// (the effect chain that triggers playback is async, not a click
	// handler). The element's play() promise still resolves fine either
	// way, so this failed completely silently. Real audio correctness
	// matters more than the orb's visual
	// fidelity, so Speaking's orb is a plain CSS pulse (see .speaking-orb
	// below) instead — actual sound was the point, the animation was
	// always secondary.
	let audioCtx: AudioContext | null = null;
	let analyser: AnalyserNode | null = null;
	let rafId: number | undefined;
	let micSourceNode: MediaStreamAudioSourceNode | null = null;
	let barLevels = $state([0.3, 0.5, 0.3, 0.5, 0.3]);
	let orbScale = $state(1);

	function ensureAudioContext(): AudioContext {
		if (!audioCtx) {
			audioCtx = new AudioContext();
			analyser = audioCtx.createAnalyser();
			analyser.fftSize = 128;
		}
		if (audioCtx.state === 'suspended') void audioCtx.resume();
		return audioCtx;
	}

	// Full teardown of everything getUserMedia/Web Audio touched for one
	// recording — called from every mediaRecorder.onstop path (send,
	// cancel, hang-up). Closing the AudioContext here (not just
	// disconnecting nodes) is deliberate: live testing found the reply
	// still sounded processed/muffled — "recording booth with a scrubber
	// on the mic" — even after playback was moved off any AnalyserNode
	// routing, which points at the OS's own voice-processing audio
	// session (opened by getUserMedia's echoCancellation/noiseSuppression/
	// autoGainControl constraints) lingering for the AudioContext's whole
	// lifetime rather than actually releasing once the mic track stops.
	// Fully closing it between rounds, not just once at call end, is the
	// most direct way to make sure nothing from the mic-input session is
	// still active while the reply plays back.
	function releaseMicAudio() {
		micStream?.getTracks().forEach((t) => t.stop());
		micStream = null;
		micSourceNode?.disconnect();
		micSourceNode = null;
		stopAnalyserLoop();
		void audioCtx?.close().catch(() => {});
		audioCtx = null;
		analyser = null;
	}

	// Same "play+immediately-pause a silent clip synchronously inside a
	// real gesture" trick as AudioPlayer.unlock() (audio.svelte.ts) —
	// duplicated rather than reused because that method is private, and
	// because it has to run here, synchronously inside this component's
	// own first tap, not inside the later async appState.readAloud() call
	// that actually triggers Speaking (by then the gesture window has
	// long closed). Once unlocked in one tab session it stays unlocked for
	// every later play() call, including AudioPlayer's own.
	function unlockPlayback() {
		const silence = new Audio(
			'data:audio/wav;base64,UklGRiYAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQIAAAAAAA=='
		);
		silence
			.play()
			.then(() => silence.pause())
			.catch(() => {});
	}

	function startAnalyserLoop() {
		if (rafId !== undefined) return;
		const data = new Uint8Array(analyser!.frequencyBinCount);
		const tick = () => {
			if (!analyser) return;
			analyser.getByteFrequencyData(data);
			const bucket = Math.floor(data.length / 5);
			const levels: number[] = [];
			for (let i = 0; i < 5; i++) {
				let sum = 0;
				for (let j = i * bucket; j < (i + 1) * bucket; j++) sum += data[j] ?? 0;
				levels.push(Math.min(1, sum / bucket / 180));
			}
			barLevels = levels.map((l) => Math.max(0.15, l));
			orbScale = 1 + (levels.reduce((a, b) => a + b, 0) / 5) * 0.22;
			rafId = requestAnimationFrame(tick);
		};
		rafId = requestAnimationFrame(tick);
	}

	function stopAnalyserLoop() {
		if (rafId !== undefined) cancelAnimationFrame(rafId);
		rafId = undefined;
		barLevels = [0.3, 0.5, 0.3, 0.5, 0.3];
		orbScale = 1;
	}

	function pickMimeType(): string {
		return MediaRecorder.isTypeSupported('audio/webm;codecs=opus') ? 'audio/webm;codecs=opus' : 'audio/webm';
	}

	async function startRecording() {
		if (phase === 'listening') return;
		if (phase === 'thinking') {
			// Interrupting mid-generation — the operator wants to redo a
			// question the moment they realize the transcript was wrong,
			// not wait out the rest of a now-pointless answer. Abandon
			// tracking of the turn being aborted (its eventual 'done', with
			// whatever partial content streamed before the stop landed,
			// still persists normally to the thread — see
			// appState.stopGeneration's doc comment — it just stops being
			// this screen's business) so it doesn't get spoken once this
			// new round's real answer comes back instead.
			appState.stopGeneration();
			assistantIndex = null;
			speakingTriggered = false;
		}
		// Tapping the mic mid-Speaking is an interrupt (mockup's "Tap the
		// mic to interrupt") — stop whatever's still playing rather than
		// layering a new recording's mic input on top of it.
		if (audioEl && !audioEl.paused) audioEl.pause();
		phase = 'listening';
		pendingStop = false;
		// Runs for the whole round (Listening -> Thinking -> Speaking), not
		// just while recording — only (re)started if nothing's already
		// ticking, so interrupting mid-Speaking (this function's other
		// caller) doesn't stack a second interval on top or reset the
		// clock back to 0.
		if (elapsedTimer === undefined) {
			elapsedSec = 0;
			elapsedTimer = setInterval(() => (elapsedSec += 1), 1000);
		}

		// getUserMedia() first, before anything else — unlockPlayback()/
		// ensureAudioContext() both do real (occasionally slow, especially
		// on iOS) audio-session setup work, and doing that ahead of the mic
		// request was adding latency between the tap and actual capture
		// start, clipping the first beat of speech (live-observed: short,
		// garbled transcripts despite a full-length hold). Nothing below
		// needs either to have run yet.
		try {
			// Dropped noiseSuppression/autoGainControl (kept only
			// echoCancellation) — live testing found garbled transcripts
			// across three different STT models (Voxtral, Parakeet, Chirp 3),
			// but feeding a clean synthesized clip straight into the same
			// running model transcribed it perfectly. That rules out model
			// choice: the problem is upstream, in what's actually getting
			// captured. noiseSuppression/autoGainControl are the more
			// aggressive, content-altering processors of the three (and on
			// macOS in particular, requesting them can hand the whole tab's
			// audio session to a voice-isolation DSP pipeline meant for
			// suppressing background noise around speech — not something
			// that's ever been verified to leave real speech content
			// intact). This is the next concrete thing to try, not a
			// confirmed fix yet.
			micStream = await navigator.mediaDevices.getUserMedia({
				audio: { echoCancellation: true }
			});
		} catch (err) {
			console.error('microphone access denied or unavailable', err);
			phase = 'idle';
			clearInterval(elapsedTimer);
			elapsedTimer = undefined;
			return;
		}

		unlockPlayback();
		ensureAudioContext();
		micSourceNode = audioCtx!.createMediaStreamSource(micStream);
		micSourceNode.connect(analyser!);
		startAnalyserLoop();

		chunks = [];
		recordingStartedAt = Date.now();
		recordedMimeType = pickMimeType();
		mediaRecorder = new MediaRecorder(micStream, { mimeType: recordedMimeType });
		mediaRecorder.ondataavailable = (e) => {
			if (e.data.size > 0) chunks.push(e.data);
		};
		mediaRecorder.onstop = () => {
			const durationMs = Date.now() - recordingStartedAt;
			const blob = new Blob(chunks, { type: recordedMimeType });
			releaseMicAudio();
			void transcribeAndSend(blob, durationMs);
		};
		mediaRecorder.start();

		// The operator already released while getUserMedia() was still
		// resolving — see pendingStop's doc comment.
		if (pendingStop) stopRecording();
	}

	function stopRecording() {
		if (phase !== 'listening') return;
		// Deliberately doesn't touch elapsedTimer — it keeps running through
		// Thinking/Speaking, see startRecording's doc comment.
		if (!mediaRecorder || mediaRecorder.state !== 'recording') {
			pendingStop = true;
			return;
		}
		mediaRecorder.stop();
	}

	function cancelCall() {
		if (mediaRecorder && mediaRecorder.state === 'recording') {
			// Drop the recording instead of sending it — same "Cancel" role
			// as the mockup's Listening-screen cancel link.
			mediaRecorder.onstop = releaseMicAudio;
			mediaRecorder.stop();
		}
		clearInterval(elapsedTimer);
		elapsedTimer = undefined;
		phase = 'idle';
	}

	// See transcribeAndSend's call site for why this exists. A generous
	// but bounded wait — stopGeneration aborting an in-flight turn is
	// normally fast (no more LLM/tool round-trips to wait out), but this
	// should never be able to hang the UI indefinitely if something goes
	// wrong server-side.
	async function waitUntilNotBusy(timeoutMs = 8000): Promise<void> {
		const start = Date.now();
		while (appState.busy && Date.now() - start < timeoutMs) {
			await new Promise((resolve) => setTimeout(resolve, 100));
		}
	}

	async function transcribeAndSend(blob: Blob, durationMs: number) {
		if (durationMs < 300 || blob.size < 500) {
			phase = 'idle';
			return;
		}
		transcribing = true;
		try {
			const res = await fetch('/api/transcribe?format=webm', { method: 'POST', body: blob });
			if (!res.ok) {
				console.error('transcription failed', await res.text());
				phase = 'idle';
				return;
			}
			const data = await res.json();
			const text = (data.text ?? '').trim();
			if (!text) {
				phase = 'idle';
				return;
			}
			lastTranscript = text;
			// If this round interrupted a still-in-flight turn (startRecording
			// called appState.stopGeneration()), the abort's own 'done' event
			// — the thing that actually flips appState.busy back to false —
			// may not have landed yet. appState.send() silently no-ops while
			// busy is true (only one turn in flight per connection at a
			// time), so wait for that to clear first rather than dropping
			// what was just said on the floor.
			await waitUntilNotBusy();
			// Deliberately skips the composer entirely and sends right away —
			// see this component's top doc comment. voiceMode: true is the
			// one thing that makes this a Transponder turn rather than an
			// ordinary typed one (nudges the model's answer length/style and
			// flips threads.used_transponder — see gateway/protocol.go's
			// ClientMessage.VoiceMode doc comment).
			appState.send(text, data.cost_usd, undefined, undefined, undefined, undefined, undefined, undefined, undefined, true);
			assistantIndex = appState.turns.length - 1;
			speakingTriggered = false;
			phase = 'thinking';
		} catch (err) {
			console.error('transcription request failed', err);
			phase = 'idle';
		} finally {
			transcribing = false;
		}
	}

	function handleMicDown() {
		if (toggleMode) return;
		void startRecording();
	}
	function handleMicUp() {
		if (toggleMode) return;
		stopRecording();
	}
	function handleMicClick() {
		if (!toggleMode) return;
		if (phase === 'listening') stopRecording();
		else void startRecording();
	}

	async function beginSpeaking(idx: number) {
		await appState.readAloud(idx);
		const t = appState.turns[idx];
		if (!t?.ttsAudioFile) {
			// Synthesis failed — the reply text is still visible on screen
			// for the beat it was shown as "Speaking" with no audio; just
			// return to idle for the next push-to-talk round.
			endRound();
		}
	}

	// The object URL backing audioEl.src, so it can be revoked instead of
	// leaking — see handlePlaybackReady's doc comment for why this exists
	// at all (fetched-and-blobbed, not just pointed at the network URL).
	let playbackBlobUrl: string | null = null;
	let fetchedFor: string | null = null;

	function revokePlaybackBlob() {
		if (playbackBlobUrl) URL.revokeObjectURL(playbackBlobUrl);
		playbackBlobUrl = null;
		fetchedFor = null;
	}

	function endRound() {
		phase = 'idle';
		assistantIndex = null;
		speakingTriggered = false;
		isPlaying = false;
		revokePlaybackBlob();
		clearInterval(elapsedTimer);
		elapsedTimer = undefined;
	}

	// Deliberately NOT routed through the AudioContext/AnalyserNode graph
	// — see the doc comment above audioCtx's declaration for why: doing
	// that made the reply completely inaudible on a real device (visible
	// text, zero sound, no error) once the AudioContext got suspended
	// during the Thinking gap. Plain, unmodified <audio> playback here
	// instead — the Speaking orb's pulse is a CSS animation (.speaking-orb
	// in the styles below), not real frequency data.
	//
	// isPlaying is driven entirely by the <audio> element's own real
	// 'playing'/'pause'/'ended' events (see the template below), not by
	// whether play()'s promise resolved or rejected — live testing found
	// play() can resolve successfully while nothing audible actually
	// happens and 'ended' never fires, so a promise-based "did it work"
	// check was both a false positive (looked fine, wasn't) and left the
	// canned pulse animation running forever with no way to tell it had
	// silently failed. The manual control below is unconditional on
	// !isPlaying, not just shown reactively after a caught error — it
	// doesn't matter *why* autoplay didn't produce sound, only whether a
	// real 'playing' event ever actually fired.
	//
	// audioEl.src is set to a fully-fetched Blob URL, not the live
	// /api/workspace/... network URL directly — live testing found the
	// reply sounded audibly distorted/"robotic" played that way, while the
	// exact same file through the normal chat's WaveformAudioPlayer (whose
	// waveform-decode step does its own full fetch() of the file first)
	// sounded completely normal. The server response is chunked transfer
	// encoding with no Content-Length (see gateway/workspace.go), which
	// some browsers appear to decode incorrectly as a live audio stream
	// but decode fine once it's a complete, fully-buffered blob — fetching
	// it fully before ever handing it to <audio> sidesteps that entirely.
	async function handlePlaybackReady(el: HTMLAudioElement, url: string) {
		if (fetchedFor === url) return; // already fetched this round
		fetchedFor = url;
		try {
			const res = await fetch(url);
			const blob = await res.blob();
			playbackBlobUrl = URL.createObjectURL(blob);
			el.src = playbackBlobUrl;
		} catch (err) {
			console.error('fetching call audio failed, falling back to direct URL', err);
			el.src = url;
		}
		el.muted = false;
		el.volume = 1;
		void el.play().catch((err) => console.error('call autoplay attempt failed (manual control still available)', err));
	}

	function retryPlayback() {
		if (!audioEl) return;
		void audioEl.play().catch((err) => console.error('call playback retry failed', err));
	}

	// Advances phase from Thinking -> Speaking the moment the assistant
	// turn this round is waiting on finishes streaming, then kicks off
	// synthesis via the same appState.readAloud() the normal chat's
	// speaker icon uses (sets turn.ttsAudioFile once persisted).
	$effect(() => {
		if (assistantIndex === null || speakingTriggered) return;
		const t = appState.turns[assistantIndex];
		if (!t || t.streaming) return;
		speakingTriggered = true;
		phase = 'speaking';
		void beginSpeaking(assistantIndex);
	});

	$effect(() => {
		if (phase === 'speaking' && audioEl && turn?.ttsAudioFile) {
			void handlePlaybackReady(audioEl, turn.ttsAudioFile);
		}
	});

	// Condensed chip label for the Thinking screen — a shorter cousin of
	// ToolEvent.svelte's label(), since this screen only ever shows a
	// glanceable "what's it doing" list, not the full expandable detail
	// the normal timeline gives each tool call. Includes reasoning bursts
	// alongside tool calls — a model that's doing extended hidden thinking
	// with no tool calls at all previously showed nothing but a static
	// "Thinking…" the whole time, which read as stuck/broken rather than
	// genuinely working.
	type ThinkingChip = Extract<TimelineItem, { kind: 'tool' }> | Extract<TimelineItem, { kind: 'reasoning' }>;
	function chipLabel(item: ThinkingChip): string {
		if (item.kind === 'reasoning') return item.done ? 'Reasoned' : 'Reasoning…';
		if (item.tool === 'web_search') return `Searching: ${item.args?.query ?? ''}`;
		if (item.tool === 'web_read') return `Reading: ${item.args?.url ?? ''}`;
		if (item.tool === 'weather') return `Checking the weather`;
		if (item.tool === 'code_exec') return 'Running code';
		return item.tool;
	}
	let toolChips = $derived(
		(turn?.timeline ?? []).filter(
			(i): i is ThinkingChip => i.kind === 'tool' || i.kind === 'reasoning'
		)
	);

	function handleClose() {
		if (mediaRecorder && mediaRecorder.state === 'recording') {
			// Drop, don't send — same reasoning as cancelCall's override
			// below. Ending the call mid-recording shouldn't fire off
			// whatever's been captured so far.
			mediaRecorder.onstop = releaseMicAudio;
			mediaRecorder.stop();
		}
		clearInterval(elapsedTimer);
		onClose();
	}

	onDestroy(() => {
		clearInterval(elapsedTimer);
		releaseMicAudio();
		revokePlaybackBlob();
	});

	function citationHost(url: string): string {
		try {
			return new URL(url).hostname.replace(/^www\./, '');
		} catch {
			return url;
		}
	}

	function formatElapsed(sec: number): string {
		const m = Math.floor(sec / 60);
		const s = sec % 60;
		return `${m}:${s.toString().padStart(2, '0')}`;
	}
</script>

<div class="transponder">
	<div class="starfield" aria-hidden="true"></div>
	<div class="header">
		<button class="icon-btn" aria-label="Close Transponder" onclick={handleClose}>
			<X size={16} />
		</button>
		<span class="phase-label" class:accent2={phase === 'thinking'}>
			{phase === 'idle' ? 'Transponder' : phase[0].toUpperCase() + phase.slice(1)}
		</span>
		{#if phase !== 'idle'}
			<span class="elapsed">{formatElapsed(elapsedSec)}</span>
		{:else}
			<div class="header-spacer"></div>
		{/if}
	</div>

	<div class="stage">
		{#if phase === 'idle' || phase === 'listening'}
			<!-- One persistent button spans Idle -> Listening (never swapped
			     for a different element) so the same gesture that starts a
			     hold-mode recording is the one whose release ends it — same
			     shape as VoiceButton.svelte's .mic-btn, and the reason this
			     follows appState.settings.voiceInputMode exactly like that
			     component does instead of having its own separate notion of
			     hold vs. toggle. Swapping to a different element mid-gesture
			     (an earlier version of this screen did) breaks touch-event
			     continuity: touchend can fail to fire at all once its
			     original target has been removed from the DOM. -->
			<button
				type="button"
				class="orb mic-orb"
				class:listening={phase === 'listening'}
				style:transform={phase === 'listening' ? `scale(${orbScale})` : undefined}
				disabled={appState.busy || transcribing}
				onclick={toggleMode ? handleMicClick : undefined}
				onmousedown={toggleMode ? undefined : handleMicDown}
				onmouseup={toggleMode ? undefined : handleMicUp}
				onmouseleave={toggleMode ? undefined : handleMicUp}
				ontouchstart={toggleMode
					? undefined
					: (e) => {
							e.preventDefault();
							handleMicDown();
						}}
				ontouchend={toggleMode
					? undefined
					: (e) => {
							e.preventDefault();
							handleMicUp();
						}}
				aria-label={toggleMode
					? phase === 'listening'
						? 'Tap to stop and send'
						: 'Tap to record'
					: 'Hold to record'}
			>
				{#if phase === 'listening' && transcribing}
					<Loader2 size={28} color="var(--color-accent)" class="spin" />
				{:else if phase === 'listening'}
					<div class="bars">
						{#each barLevels as level, i (i)}
							<div class="bar" style:height="{16 + level * 40}px"></div>
						{/each}
					</div>
				{:else}
					<Mic size={40} color="var(--color-accent)" />
				{/if}
			</button>
			<div class="idle-copy">
				{#if phase === 'idle'}
					<div class="idle-title">Talk to Polaris</div>
					<div class="idle-sub">
						{toggleMode ? 'Tap the mic and start speaking' : 'Hold the mic and start speaking'}
					</div>
				{:else}
					<div class="idle-sub">
						{toggleMode ? 'Tap the mic to stop and send' : 'Release the mic to send'}
					</div>
					<button class="cancel-link" onclick={cancelCall}>Cancel</button>
				{/if}
			</div>
		{:else if phase === 'thinking'}
			<!-- Interruptible too, same orb-is-the-mic pattern as Speaking —
			     the operator needs to be able to redo a question the moment
			     they realize the transcript was wrong, without waiting out
			     the rest of the (now-pointless) generation first. -->
			<button
				type="button"
				class="orb thinking-orb"
				onclick={toggleMode ? handleMicClick : undefined}
				onmousedown={toggleMode ? undefined : handleMicDown}
				onmouseup={toggleMode ? undefined : handleMicUp}
				onmouseleave={toggleMode ? undefined : handleMicUp}
				ontouchstart={toggleMode
					? undefined
					: (e) => {
							e.preventDefault();
							handleMicDown();
						}}
				ontouchend={toggleMode
					? undefined
					: (e) => {
							e.preventDefault();
							handleMicUp();
						}}
				aria-label={toggleMode ? 'Tap to interrupt and record' : 'Hold to interrupt and record'}
			>
				<div class="spinner"></div>
				<Loader2 size={26} color="var(--color-accent-2)" class="spin" />
			</button>
			{#if lastTranscript}
				<div class="transcript-bubble">
					<span class="transcript-label">You said</span>
					{lastTranscript}
				</div>
			{/if}
			{#if toolChips.length > 0}
				<div class="chip-list">
					{#each toolChips as item, i (i)}
						<div class="chip">
							{#if !item.done}
								<Loader2 size={11} color="var(--color-accent-2)" class="spin" />
							{/if}
							{chipLabel(item)}
						</div>
					{/each}
				</div>
			{:else}
				<div class="thinking-copy">Thinking…</div>
			{/if}
		{:else if phase === 'speaking'}
			<!-- The orb itself is the tap target here too, same as Idle/
			     Listening (see .mic-orb's doc comment) — a separate small
			     footer icon for this used to be the only way to start a new
			     round mid-Speaking, and it was easy to miss entirely (no
			     label, low-contrast, tucked in a corner). One consistent
			     "the orb is always the mic" mental model instead. -->
			<button
				type="button"
				class="orb speaking-orb"
				class:paused={!isPlaying}
				onclick={toggleMode ? handleMicClick : undefined}
				onmousedown={toggleMode ? undefined : handleMicDown}
				onmouseup={toggleMode ? undefined : handleMicUp}
				onmouseleave={toggleMode ? undefined : handleMicUp}
				ontouchstart={toggleMode
					? undefined
					: (e) => {
							e.preventDefault();
							handleMicDown();
						}}
				ontouchend={toggleMode
					? undefined
					: (e) => {
							e.preventDefault();
							handleMicUp();
						}}
				aria-label={toggleMode ? 'Tap to interrupt and record' : 'Hold to interrupt and record'}
			>
				<div class="bars">
					{#each { length: 5 } as _, i (i)}
						<div class="bar speaking-bar" style:animation-delay="{i * 0.1}s"></div>
					{/each}
				</div>
			</button>
			{#if turn?.ttsAudioFile && !isPlaying}
				<!-- Unconditional on !isPlaying, not just shown after a caught
				     error — see handlePlaybackReady's doc comment on why
				     detecting the failure mode reliably isn't possible, so
				     this is the one guaranteed-working path instead. -->
				<button class="tap-to-play-btn" onclick={retryPlayback}>
					<Volume2 size={14} />
					Tap to hear it
				</button>
			{/if}
			{#if lastTranscript}
				<div class="transcript-bubble">
					<span class="transcript-label">You said</span>
					{lastTranscript}
				</div>
			{/if}
			{#if turn?.content}
				<div class="reply-card">{turn.content}</div>
			{/if}
			{#if turn?.citations && turn.citations.length > 0}
				<div class="citation-row">
					{#each turn.citations.slice(0, 4) as c, i (i)}
						<div class="citation-chip">{c.site_name || citationHost(c.url)}</div>
					{/each}
				</div>
			{/if}
			{#if turn?.ttsAudioFile}
				<!-- src is set imperatively in handlePlaybackReady (a fetched
				     Blob URL, not this src attribute) — see that function's
				     doc comment. -->
				<!-- svelte-ignore a11y_media_has_caption -->
				<audio
					bind:this={audioEl}
					onplaying={() => (isPlaying = true)}
					onpause={() => (isPlaying = false)}
					onended={() => (isPlaying = false)}
					onerror={() => (isPlaying = false)}
				></audio>
			{/if}
		{/if}
	</div>

	<div class="footer">
		{#if phase !== 'idle'}
			<div class="footer-row">
				<button class="end-call-btn" onclick={handleClose}>
					<PhoneOff size={16} />
					End Call
				</button>
			</div>
		{/if}
	</div>
</div>

<style>
	.transponder {
		position: fixed;
		inset: 0;
		z-index: var(--z-modal);
		display: flex;
		flex-direction: column;
		background:
			radial-gradient(circle at 18% 8%, color-mix(in srgb, var(--color-surface-3) 55%, transparent), transparent 45%),
			radial-gradient(circle at 85% 92%, color-mix(in srgb, var(--color-accent-2) 20%, transparent), transparent 40%),
			var(--color-bg);
	}

	.starfield {
		position: absolute;
		inset: 0;
		pointer-events: none;
		opacity: 0.5;
		background-image:
			radial-gradient(1px 1px at 12% 18%, var(--color-text-dim), transparent),
			radial-gradient(1px 1px at 82% 12%, var(--color-text-dim), transparent),
			radial-gradient(1px 1px at 34% 6%, var(--color-text-dim), transparent),
			radial-gradient(1px 1px at 62% 22%, var(--color-text-dim), transparent),
			radial-gradient(1px 1px at 91% 44%, var(--color-text-dim), transparent),
			radial-gradient(1px 1px at 6% 55%, var(--color-text-dim), transparent),
			radial-gradient(1px 1px at 48% 82%, var(--color-text-dim), transparent),
			radial-gradient(1px 1px at 76% 70%, var(--color-text-dim), transparent);
	}

	.header {
		position: relative;
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: var(--space-xl) var(--space-xl) 0 var(--space-xl);
	}

	.icon-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 38px;
		height: 38px;
		border-radius: var(--radius-full);
		border: 1px solid var(--color-border);
		background: var(--color-surface-2);
		color: var(--color-text-dim);
	}

	.phase-label {
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.08em;
		color: var(--color-accent);
		text-transform: uppercase;
	}

	.phase-label.accent2 {
		color: var(--color-accent-2);
	}

	.elapsed {
		font-size: 13px;
		color: var(--color-text-dim);
		font-variant-numeric: tabular-nums;
		width: 38px;
		text-align: right;
	}

	.header-spacer {
		width: 38px;
		height: 38px;
	}

	.stage {
		position: relative;
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-2xl);
		padding: 0 var(--space-2xl);
		/* Safety net beyond the transcript bubble's own internal scroll cap
		   — a long reply + citations + a capped transcript together could
		   still exceed a short viewport (small phone, landscape). Scrolls
		   internally instead of clipping past the fixed header/footer. */
		overflow-y: auto;
	}

	.orb {
		border-radius: var(--radius-full);
		display: flex;
		align-items: center;
		justify-content: center;
		background: radial-gradient(circle at 35% 30%, var(--color-surface-3), var(--color-surface) 70%);
		border: 1px solid var(--color-border-strong);
		transition: transform 0.08s linear;
	}

	/* The push-to-talk control itself in Idle/Listening — a real <button>,
	   not a decorative div, so it can carry the same hold/tap handlers as
	   VoiceButton.svelte's .mic-btn (see the template's doc comment on why
	   this stays one element across both phases). */
	.mic-orb {
		width: 168px;
		height: 168px;
		padding: 0;
		cursor: pointer;
		animation: breathe 3.6s var(--ease-out-expo) infinite;
	}

	.mic-orb:disabled {
		opacity: 0.5;
		cursor: default;
	}

	.mic-orb.listening {
		width: 148px;
		height: 148px;
		border-color: color-mix(in srgb, var(--color-accent) 45%, transparent);
		animation: none;
	}

	.thinking-orb {
		position: relative;
		width: 148px;
		height: 148px;
		padding: 0;
		cursor: pointer;
		border-color: var(--color-border-strong);
	}

	.speaking-orb {
		width: 128px;
		height: 128px;
		padding: 0;
		cursor: pointer;
		border-color: color-mix(in srgb, var(--color-accent) 45%, transparent);
		animation: pulse-ring 1.8s var(--ease-out-expo) infinite;
	}

	/* !isPlaying: freeze the pulse instead of animating away with nothing
	   actually playing — a moving orb over silence read as "still
	   working" rather than what it actually was, "stuck". */
	.speaking-orb.paused {
		animation-play-state: paused;
		opacity: 0.6;
	}

	.speaking-orb.paused .speaking-bar {
		animation-play-state: paused;
	}

	/* Canned, not audio-reactive — see the doc comment above
	   handlePlaybackReady for why real playback correctness won out over
	   wiring this to live frequency data. */
	.speaking-bar {
		height: 30px;
		animation: bar-bounce 0.7s ease-in-out infinite;
	}

	.tap-to-play-btn {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		background: var(--color-accent-soft);
		border: 1px solid color-mix(in srgb, var(--color-accent) 45%, transparent);
		color: var(--color-accent);
		padding: 8px 16px;
		border-radius: var(--radius-full);
		font-size: 13px;
		font-weight: 600;
	}

	@keyframes bar-bounce {
		0%, 100% { transform: scaleY(0.4); }
		50% { transform: scaleY(1); }
	}

	@keyframes pulse-ring {
		0%, 100% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--color-accent) 22%, transparent); }
		50% { box-shadow: 0 0 0 10px transparent; }
	}

	.spinner {
		position: absolute;
		inset: 0;
		border-radius: var(--radius-full);
		border: 2px solid transparent;
		border-top-color: var(--color-accent-2);
		border-right-color: color-mix(in srgb, var(--color-accent-2) 25%, transparent);
		animation: spin 1.6s linear infinite;
	}

	.bars {
		display: flex;
		align-items: center;
		gap: 4px;
	}

	.bar {
		width: 4px;
		border-radius: 2px;
		background: var(--color-accent);
		transition: height 0.06s linear;
	}

	.idle-copy {
		text-align: center;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.idle-title {
		font-size: 20px;
		color: var(--color-text);
	}

	.idle-sub {
		font-size: 14px;
		color: var(--color-text-dim);
	}

	.thinking-copy {
		font-size: 15px;
		color: var(--color-text-dim);
		font-style: italic;
	}

	.chip-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		max-width: 300px;
	}

	.chip {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		color: var(--color-text-dim);
		background: var(--color-surface-3);
		border-radius: var(--radius-full);
		padding: 6px 10px;
		width: fit-content;
	}

	.reply-card {
		width: 100%;
		max-width: 320px;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		padding: var(--space-lg) var(--space-lg);
		font-size: 15px;
		line-height: 1.55;
		color: var(--color-text);
	}

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

	.citation-row {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		justify-content: center;
		max-width: 320px;
	}

	.citation-chip {
		font-size: 11px;
		color: var(--color-text-dim);
		background: var(--color-surface-3);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-full);
		padding: 5px 10px;
	}

	.cancel-link {
		background: none;
		border: none;
		color: var(--color-text-dim);
		font-size: 13px;
		text-decoration: underline;
		text-underline-offset: 3px;
		padding: 4px;
	}

	.footer {
		position: relative;
		display: flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-lg);
		padding: 0 var(--space-xl) var(--space-4xl) var(--space-xl);
	}

	.footer-row {
		display: flex;
		align-items: center;
		gap: var(--space-lg);
	}

	.end-call-btn {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		background: var(--color-danger-bg);
		border: 1px solid color-mix(in srgb, var(--color-danger) 45%, transparent);
		color: color-mix(in srgb, var(--color-danger) 30%, white);
		padding: 12px 22px;
		border-radius: var(--radius-full);
		font-size: 14px;
		font-weight: 600;
	}

	@keyframes breathe {
		0%, 100% { transform: scale(1); }
		50% { transform: scale(1.03); }
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	:global(.transponder .spin) {
		animation: spin 1s linear infinite;
	}

	@media (prefers-reduced-motion: reduce) {
		.mic-orb,
		.speaking-orb,
		.speaking-bar {
			animation: none;
		}
	}
</style>
