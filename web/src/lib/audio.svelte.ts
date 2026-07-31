import type { ChatTurn } from './types';
import { synthesizeStream } from './speech';

// Manual per-message read-aloud, split out of state.svelte.ts since it's a
// self-contained concern (playback state + one async action) with exactly
// one consumer (ChatTurnView's speaker icon). speakingIndex is set the
// instant synthesis starts (fetching); isPlaying flips true only once
// audio actually starts playing — the button needs both to distinguish
// "loading" from "playing, click to stop" from "idle".
//
// Playback is chunked (see speech.ts's synthesizeStream): the answer is
// synthesized sentence-by-sentence, and each chunk queues up and plays as
// soon as it arrives rather than waiting for the whole answer to finish
// synthesizing — noticeably faster time-to-first-audio on a long answer.
export class AudioPlayer {
	speakingIndex = $state<number | null>(null);
	isPlaying = $state(false);

	private queue: HTMLAudioElement[] = [];
	private currentAudio: HTMLAudioElement | null = null;
	// True once synthesizeStream's promise has resolved — until then, an
	// empty queue with nothing currently playing just means playback has
	// caught up to synthesis, not that the session is over.
	private streamDone = false;
	// Bumped on every stop()/new readAloud call so chunks that arrive
	// after a stop (the fetch was already in flight) recognize they're
	// stale and don't get queued or played.
	private sessionToken = 0;

	// Clicking the turn that's already active (loading OR playing) stops
	// it — a toggle, not just a one-way trigger. onCost reports the
	// synthesis session's total billed cost back to the caller (folded
	// into the thread's running total) since this class has no thread
	// state of its own.
	async readAloud(turns: ChatTurn[], assistantTurnIndex: number, threadId: string | null, onCost: (cost: number) => void) {
		if (this.speakingIndex === assistantTurnIndex) {
			this.stop();
			return;
		}

		const turn = turns[assistantTurnIndex];
		if (!turn || turn.role !== 'assistant' || !turn.content) return;

		this.stop(); // only one read-aloud plays at a time
		const token = ++this.sessionToken;
		this.speakingIndex = assistantTurnIndex;
		this.streamDone = false;

		const result = await synthesizeStream(turn.content, threadId ?? undefined, (audio) => {
			if (token !== this.sessionToken) {
				// Stopped (or a different turn started reading) while this
				// chunk was still in flight — don't resurrect playback.
				URL.revokeObjectURL(audio.src);
				return;
			}
			this.enqueue(audio);
		});

		if (token !== this.sessionToken) return; // superseded mid-stream
		this.streamDone = true;
		if (result.cost) onCost(result.cost);
		if (result.error) {
			console.error('TTS stream ended early', result.error);
		}
		this.maybeFinish();
	}

	private enqueue(audio: HTMLAudioElement) {
		audio.onended = () => {
			if (this.currentAudio !== audio) return;
			this.currentAudio = null;
			this.playNext();
		};
		this.queue.push(audio);
		if (!this.currentAudio) this.playNext();
	}

	// Plays the next queued chunk, if any; otherwise checks whether the
	// whole session (all chunks played, streaming finished) just ended.
	private playNext() {
		const next = this.queue.shift();
		if (!next) {
			this.maybeFinish();
			return;
		}
		this.currentAudio = next;
		next
			.play()
			.then(() => {
				this.isPlaying = true;
			})
			.catch((err) => {
				// One chunk failing to play (autoplay policy, decode error)
				// shouldn't kill the rest of the answer — skip to the next.
				console.error('audio playback failed', err);
				this.currentAudio = null;
				this.playNext();
			});
	}

	// The session is fully over only once synthesis has finished AND
	// every synthesized chunk has played — an empty queue alone can just
	// mean playback is briefly waiting on the next chunk to arrive.
	private maybeFinish() {
		if (this.streamDone && this.queue.length === 0 && !this.currentAudio) {
			this.isPlaying = false;
			this.speakingIndex = null;
		}
	}

	stop() {
		this.sessionToken++; // any in-flight synthesizeStream chunks become stale
		if (this.currentAudio) {
			this.currentAudio.onended = null;
			this.currentAudio.pause();
			this.currentAudio = null;
		}
		for (const audio of this.queue) {
			URL.revokeObjectURL(audio.src);
		}
		this.queue = [];
		this.streamDone = true;
		this.isPlaying = false;
		this.speakingIndex = null;
	}
}
