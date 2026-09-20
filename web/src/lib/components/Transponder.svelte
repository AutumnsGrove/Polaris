<script lang="ts">
	import { onDestroy } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import { synthesizeStream } from '$lib/speech';
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
	// 'playing'/'pause'/'ended' events — see beginSpeaking's doc comment
	// for why this replaced a play()-promise-based check. The file itself
	// checks out fine server-side (verified directly against a real
	// persisted .wav: ~85% non-zero PCM payload, valid header), so a
	// failure here is a browser/playback-state problem, not a broken file.
	let isPlaying = $state(false);
	// True from the moment the first chunk of this round's reply exists —
	// drives the "Tap to hear it" fallback's visibility (see the template)
	// independent of whether the ENTIRE reply has finished synthesizing.
	let hasAnyChunkToPlay = $state(false);
	// Bumped every time a new round starts speaking; a chunk-arrival
	// callback or the final synthesizeStream() result checks its own
	// captured token against this before acting, so an interrupted
	// round's still-in-flight network response can't hijack audioEl once
	// the operator has already moved on to a new question — mirrors
	// AudioPlayer.readAloud's own sessionToken pattern (audio.svelte.ts).
	let speakGeneration = 0;
	let pendingChunks: { url: string; blob: Blob }[] = [];
	let currentChunkUrl: string | null = null;
	let chunkInFlight = false;
	let streamFullyReceived = false;

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

	// Real amplitude data for the Speaking orb, decoded once per reply —
	// same technique WaveformAudioPlayer.svelte already uses for its own
	// waveform (fetch -> decodeAudioData -> bucket into peaks), not a live
	// AnalyserNode tap on the playing <audio> element. Deliberately not
	// that: routing actual playback through Web Audio (createMediaElement
	// Source) is exactly what caused the total-silence bug earlier in this
	// project (see the doc comment above). Pre-decoding a static peaks
	// array and sweeping through it against audioEl.currentTime gets real,
	// audio-reflective bars with zero risk to playback itself — the decode
	// context is a throwaway, closed immediately, never touches the
	// element actually making sound.
	let playbackPeaks: number[] = [];
	const playbackPeakResolutionMs = 25;

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

	// Peak amplitude per playbackPeakResolutionMs-wide bucket, normalized
	// against this clip's own loudest bucket and exponent-exaggerated —
	// mirrors WaveformAudioPlayer.svelte's decodePeaks() almost exactly
	// (same reasoning: Kokoro's output has a narrow dynamic range, so
	// stretching each clip's own peaks to fill 0..1 first reads far less
	// flat than a fixed-constant normalization would).
	function computePeaks(buffer: AudioBuffer): number[] {
		const channel = buffer.getChannelData(0);
		const bucketSize = Math.max(1, Math.floor((playbackPeakResolutionMs / 1000) * buffer.sampleRate));
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

	// How far apart (in buckets) the 5 sampled bars are spread — see
	// startPlaybackVisualizer's doc comment for why this isn't 1 (adjacent
	// buckets).
	const barStrideBuckets = 8;

	// Sweeps the 5 orb bars through playbackPeaks in step with the
	// <audio> element's real currentTime — genuinely reflects what's
	// playing, not a canned loop with no relationship to the actual audio.
	// Deliberately samples across a WIDE spread (barStrideBuckets apart,
	// ~200ms at the 25ms bucket resolution above — ~800ms of history
	// across all 5 bars), not 5 adjacent buckets: ordinary speech barely
	// changes amplitude within a quarter-second, so an adjacent-bucket
	// version looked nearly flat/jittery most of the time and only
	// twitched on loud syllable onsets — a real bug caught live, not just
	// a tuning nitpick. Shares rafId/barLevels/stopAnalyserLoop with the
	// Listening-side mic visualizer above since only one is ever running
	// at a time (Listening and Speaking can't overlap).
	function startPlaybackVisualizer() {
		if (rafId !== undefined) return;
		const tick = () => {
			if (!audioEl || playbackPeaks.length === 0) {
				rafId = requestAnimationFrame(tick);
				return;
			}
			const centerIdx = Math.floor((audioEl.currentTime * 1000) / playbackPeakResolutionMs);
			const levels: number[] = [];
			for (let i = 4; i >= 0; i--) {
				levels.push(playbackPeaks[Math.max(0, centerIdx - i * barStrideBuckets)] ?? 0.1);
			}
			barLevels = levels;
			orbScale = 1 + (levels.reduce((a, b) => a + b, 0) / 5) * 0.22;
			rafId = requestAnimationFrame(tick);
		};
		rafId = requestAnimationFrame(tick);
	}

	function pickMimeType(): string {
		return MediaRecorder.isTypeSupported('audio/webm;codecs=opus') ? 'audio/webm;codecs=opus' : 'audio/webm';
	}

	async function startRecording() {
		if (phase === 'listening') return;
		if (phase === 'thinking' || phase === 'speaking') {
			if (phase === 'thinking') {
				// Interrupting mid-generation — the operator wants to redo a
				// question the moment they realize the transcript was wrong,
				// not wait out the rest of a now-pointless answer. Whatever
				// partial content streamed before the stop landed still
				// persists normally to the thread — see
				// appState.stopGeneration's doc comment — it just stops
				// being this screen's business.
				appState.stopGeneration();
			}
			// Tapping the mic mid-Speaking is an interrupt too (mockup's
			// "Tap the mic to interrupt") — bump speakGeneration so any
			// chunk still arriving over the network for the turn being
			// abandoned gets ignored (its callback checks this token)
			// instead of hijacking audioEl mid-new-recording, and fully
			// release whatever was queued/playing rather than leaving it
			// dangling.
			speakGeneration++;
			assistantIndex = null;
			speakingTriggered = false;
			isPlaying = false;
			hasAnyChunkToPlay = false;
			chunkInFlight = false;
			revokeAllChunkUrls();
			if (audioEl && !audioEl.paused) audioEl.pause();
		}
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
			// No constraints at all now — dropped noiseSuppression/
			// autoGainControl first (transcription quality turned out to be
			// a mic-distance/ambient-noise thing instead, not this), and now
			// echoCancellation too. Live testing described reply *playback*
			// in Transponder as sounding phase-doubled/reverberated — "like
			// a second version playing on top of the first with a ~25ms
			// delay" — which is a textbook comb-filter/phasing artifact, not
			// a compression-quality complaint. echoCancellation (AEC) works
			// by comparing mic input against a reference tap of the
			// system's own audio *output* to cancel feedback; some OS/
			// browser AEC implementations are known to leak a delayed copy
			// of that reference tap audibly back into output. Given the
			// exact symptom described, that reference-tap mechanism is the
			// most likely remaining culprit — still unconfirmed, next thing
			// to test.
			micStream = await navigator.mediaDevices.getUserMedia({ audio: true });
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

	function revokeAllChunkUrls() {
		if (currentChunkUrl) URL.revokeObjectURL(currentChunkUrl);
		currentChunkUrl = null;
		for (const c of pendingChunks) URL.revokeObjectURL(c.url);
		pendingChunks = [];
	}

	function endRound() {
		phase = 'idle';
		assistantIndex = null;
		speakingTriggered = false;
		isPlaying = false;
		hasAnyChunkToPlay = false;
		chunkInFlight = false;
		revokeAllChunkUrls();
		clearInterval(elapsedTimer);
		elapsedTimer = undefined;
	}

	// Progressive playback: talks to /api/speak/stream directly (via
	// synthesizeStream's onChunk callback) instead of going through
	// appState.readAloud()/AudioPlayer, which always waits for the whole
	// answer before returning anything playable. Each chunk is a complete,
	// independently-decodable WAV (see SpeechChunk's doc comment in
	// speech.ts), so chaining them through the same <audio> element one at
	// a time via playNextQueuedChunk/handleChunkEnded needs no gapless-PCM
	// stitching — this is exactly the time-to-first-audio behavior this
	// codebase had once before and removed for normal chat's read-aloud
	// (too many chunks, a scrubber to keep correct); voice_mode answers
	// are short enough (1-3 sentences) that reviving it here is a much
	// smaller, lower-risk surface. Deliberately NOT routed through the
	// AudioContext/AnalyserNode graph either way — see audioCtx's own doc
	// comment for why that made a whole answer silently inaudible before.
	//
	// speakGeneration guards every callback below against acting on a
	// round the operator has since interrupted (see startRecording's
	// speakGeneration bump) — without it, a chunk still arriving over the
	// network for an abandoned turn could hijack audioEl mid a completely
	// different new recording.
	async function beginSpeaking(idx: number) {
		const t = appState.turns[idx];
		if (!t || t.role !== 'assistant' || !t.content) {
			endRound();
			return;
		}
		const token = ++speakGeneration;
		pendingChunks = [];
		currentChunkUrl = null;
		chunkInFlight = false;
		streamFullyReceived = false;
		hasAnyChunkToPlay = false;
		playbackPeaks = [];

		const result = await synthesizeStream(t.content, appState.currentThreadId ?? undefined, t.id, (chunk) => {
			if (token !== speakGeneration) return; // this round was interrupted — ignore
			enqueueChunk(chunk.audioBase64, chunk.contentType);
		});
		if (token !== speakGeneration) return; // interrupted before the stream even finished
		streamFullyReceived = true;

		if (result.cost) {
			// Mirrors AudioPlayer.readAloud's own bookkeeping (audio.svelte.ts)
			// — this bypasses that class entirely (it has no progressive-chunk
			// support), so its two side effects worth keeping are replicated
			// by hand: the thread-wide running total, and this turn's own
			// visible cost badge.
			appState.totalCost += result.cost;
			t.costUsd = (t.costUsd ?? 0) + result.cost;
		}
		if (result.error) {
			console.error('call TTS stream ended with an error', result.error);
		}
		if (result.file) {
			// For ChatView's later normal display on reload/after hanging
			// up — Transponder's own playback here never reads this field.
			t.ttsAudioFile = result.file;
		}

		if (!hasAnyChunkToPlay) {
			// Nothing ever synthesized — no reply to speak at all. If chunks
			// DID arrive, playback is already underway/chained on its own
			// via handleChunkEnded; nothing more to do here in that case.
			endRound();
		}
	}

	function enqueueChunk(base64: string, contentType: string) {
		if (!audioEl) return;
		hasAnyChunkToPlay = true;
		const binary = atob(base64);
		const bytes = new Uint8Array(binary.length);
		for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
		const blob = new Blob([bytes], { type: contentType });
		pendingChunks.push({ url: URL.createObjectURL(blob), blob });
		if (!chunkInFlight) playNextQueuedChunk();
	}

	function playNextQueuedChunk() {
		if (!audioEl) return;
		const next = pendingChunks.shift();
		if (!next) {
			chunkInFlight = false;
			return;
		}
		chunkInFlight = true;
		if (currentChunkUrl) URL.revokeObjectURL(currentChunkUrl);
		currentChunkUrl = next.url;
		audioEl.src = next.url;
		audioEl.muted = false;
		audioEl.volume = 1;
		void audioEl.play().catch((err) => {
			console.error('call chunk playback failed (manual control still available)', err);
			chunkInFlight = false;
		});
		// Decoded separately, after kicking off play() above — never let
		// waveform decoding delay actual playback starting.
		void decodePlaybackPeaks(next.blob);
	}

	// Fires on the shared <audio> element's real 'ended' event — chains to
	// whatever's queued next, or leaves chunkInFlight false to wait: either
	// more are still arriving (enqueueChunk resumes playback itself once
	// one lands) or streamFullyReceived is already true and this really is
	// the end of the reply, in which case there's nothing left to do —
	// same "stay on Speaking, don't auto-return to Idle" behavior as
	// before applies either way.
	function handleChunkEnded() {
		chunkInFlight = false;
		if (pendingChunks.length > 0) playNextQueuedChunk();
	}

	async function decodePlaybackPeaks(blob: Blob) {
		let decodeCtx: AudioContext | undefined;
		try {
			decodeCtx = new AudioContext();
			const arrayBuffer = await blob.arrayBuffer();
			const buffer = await decodeCtx.decodeAudioData(arrayBuffer);
			playbackPeaks = computePeaks(buffer);
		} catch (err) {
			// Cosmetic only — the orb just falls back to its resting state
			// (startPlaybackVisualizer's own empty-array guard), playback
			// itself is entirely unaffected.
			console.error('decoding call audio for orb waveform failed', err);
		} finally {
			void decodeCtx?.close().catch(() => {});
		}
	}

	function retryPlayback() {
		if (!audioEl) return;
		void audioEl.play().catch((err) => console.error('call playback retry failed', err));
	}

	// Advances phase from Thinking -> Speaking the moment the assistant
	// turn this round is waiting on finishes streaming, then kicks off
	// progressive synthesis/playback — see beginSpeaking's own doc comment.
	$effect(() => {
		if (assistantIndex === null || speakingTriggered) return;
		const t = appState.turns[assistantIndex];
		if (!t || t.streaming) return;
		speakingTriggered = true;
		phase = 'speaking';
		void beginSpeaking(assistantIndex);
	});

	// Drives the Speaking orb's bars from real decoded peak data while
	// audio is genuinely playing (see isPlaying's own doc comment on why
	// that's real-event-driven, not assumed) — same stopAnalyserLoop reset
	// the Listening side uses once playback pauses/ends.
	$effect(() => {
		if (isPlaying) startPlaybackVisualizer();
		else stopAnalyserLoop();
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
	let chipListEl: HTMLDivElement | undefined = $state();
	// Keeps the newest chip in view as more stream in — without this, once
	// the list is taller than its capped max-height, a fresh chip appends
	// below the fold and the "what's happening right now" signal this
	// screen exists for goes invisible again, just via a different
	// mechanism than the original unbounded-height bug.
	$effect(() => {
		toolChips.length;
		queueMicrotask(() => chipListEl?.scrollTo({ top: chipListEl.scrollHeight, behavior: 'smooth' }));
	});

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
		revokeAllChunkUrls();
		// Any chunk still arriving over the network after the component's
		// already gone must not touch a detached audioEl — bind:this
		// doesn't get nulled out on unmount, so without this a phantom
		// chunk could still start "playing" nobody can see or stop.
		speakGeneration++;
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
				<div class="chip-list" bind:this={chipListEl}>
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
				style:transform={isPlaying ? `scale(${orbScale})` : undefined}
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
				<!-- Real decoded-peaks data via barLevels, not a canned loop
				     — see startPlaybackVisualizer's doc comment. -->
				<div class="bars">
					{#each barLevels as level, i (i)}
						<div class="bar" style:height="{14 + level * 34}px"></div>
					{/each}
				</div>
			</button>
			{#if hasAnyChunkToPlay && !isPlaying}
				<!-- Unconditional on !isPlaying, not just shown after a caught
				     error — see beginSpeaking's doc comment on why
				     detecting the failure mode reliably isn't possible, so
				     this is the one guaranteed-working path instead.
				     hasAnyChunkToPlay (not turn?.ttsAudioFile, which only
				     becomes true once the ENTIRE reply has finished
				     synthesizing) so this is available the moment the first
				     chunk exists, same as playback itself now is. -->
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
			<!-- Always mounted in Speaking (not gated on a chunk existing yet)
			     — src is set imperatively in playNextQueuedChunk, one chunk
			     at a time, as each arrives; see beginSpeaking's doc comment
			     for the whole progressive-playback design. -->
			<!-- svelte-ignore a11y_media_has_caption -->
			<audio
				bind:this={audioEl}
				onplaying={() => (isPlaying = true)}
				onpause={() => (isPlaying = false)}
				onended={handleChunkEnded}
				onerror={() => (isPlaying = false)}
			></audio>
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

	/* The wordmark font (Asimovian) — used sparingly, in the same
	   deliberate-accent-moment spirit as "Ask Polaris" on the welcome
	   screen, not as a body font. Sized up from a plain small caps label
	   since Asimovian is a chunky display face that reads cramped/wrong at
	   11px; letter-spacing pulled back to match, since the font already
	   carries its own visual weight. */
	.phase-label {
		font-family: var(--font-wordmark);
		font-size: 15px;
		font-weight: 400;
		letter-spacing: 0.04em;
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
		/* safe center, not plain center — a real bug caught live: with
		   plain `center` + overflow-y: auto, content taller than the
		   viewport gets pushed ABOVE the visible/scrollable area by the
		   centering itself, and that portion becomes unreachable by
		   scrolling in most browsers (the classic "centered flexbox
		   content can't be scrolled to" trap). `safe center` falls back to
		   flex-start alignment specifically when content would overflow,
		   so nothing ever ends up in unreachable space. */
		justify-content: safe center;
		gap: var(--space-2xl);
		padding: var(--space-xl) var(--space-2xl);
		/* Safety net beyond .reply-card/.transcript-bubble's own internal
		   scroll caps — several long elements together could still exceed
		   a short viewport (small phone, landscape). Scrolls internally
		   instead of clipping past the fixed header/footer. */
		overflow-y: auto;
	}

	.orb {
		border-radius: var(--radius-full);
		display: flex;
		align-items: center;
		justify-content: center;
		background: radial-gradient(circle at 35% 30%, var(--color-surface-3), var(--color-surface) 70%);
		border: 1px solid var(--color-border-strong);
		/* No transition on transform, deliberately — style:transform here
		   is already updated by JS on every animation frame (~60fps) while
		   audio is active. A CSS transition trying to interpolate toward a
		   target a 60fps loop keeps yanking away is a real, confirmed-live
		   source of visual tearing/snapping ("the element stretching too
		   far") — the orb's own pulse-ring box-shadow keyframe (removed)
		   was a second, independent animation compounding the same
		   problem, running on its own uncoordinated 1.8s schedule against
		   the real scale updates. JS driving the value every frame IS the
		   animation; a second system animating toward it just fights it. */
		/* Also fixes: a brief system "move/drag" cursor (renders as a
		   crosshair/plus in some browsers) flashed over the orb during a
		   press-and-hold — the orb's own bar children change height every
		   animation frame while held, and without this the browser can
		   read a mousedown-and-hold over fast-changing content as an
		   ambiguous drag/selection attempt and show its own drag-affordance
		   cursor instead of the plain pointer this button actually wants. */
		user-select: none;
		-webkit-user-select: none;
		-webkit-user-drag: none;
	}

	.bars,
	.bar {
		user-select: none;
		-webkit-user-select: none;
		-webkit-user-drag: none;
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
	}

	/* !isPlaying: dim to signal "not producing anything right now" — a
	   moving orb over silence read as "still working" rather than what it
	   actually was, "stuck". */
	.speaking-orb.paused {
		opacity: 0.6;
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
		/* Capped and internally scrollable, same reasoning as .reply-card/
		   .transcript-bubble — a research-heavy turn can run to a dozen+
		   web_search/web_read chips (live-caught, see the screenshot this
		   was reported from), which without a cap just kept growing and
		   crushed everything else in .stage regardless of the safe-center
		   fix. Auto-scrolls to the newest chip (see the chipList bind:this
		   + $effect below) so the live "what's happening right now" chip
		   is always the one visible, not buried above the fold. */
		max-height: 220px;
		overflow-y: auto;
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
		/* Capped and internally scrollable — voice_mode_instruction usually
		   keeps answers short, but not always (live-caught: a "that's not a
		   real thing" clarifying answer ran to a full multi-paragraph
		   reply). Same reasoning as .transcript-bubble's own cap: this
		   can't be allowed to just expand forever and push the orb and
		   footer around. */
		max-height: 280px;
		overflow-y: auto;
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
		.speaking-orb {
			animation: none;
		}
	}
</style>
