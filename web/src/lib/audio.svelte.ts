import type { ChatTurn } from './types';
import { synthesize } from './speech';

// Manual per-message read-aloud, split out of state.svelte.ts since it's a
// self-contained concern (playback state + one async action) with exactly
// one consumer (ChatTurnView's speaker icon). speakingIndex is set the
// instant synthesis starts (fetching); isPlaying flips true only once
// audio actually starts playing — the button needs both to distinguish
// "loading" from "playing, click to stop" from "idle".
export class AudioPlayer {
	speakingIndex = $state<number | null>(null);
	isPlaying = $state(false);
	private currentAudio: HTMLAudioElement | null = null;

	// Clicking the turn that's already active (loading OR playing) stops
	// it — a toggle, not just a one-way trigger. onCost reports a
	// synthesis call's billed cost back to the caller (folded into the
	// thread's running total) since this class has no thread state of its
	// own.
	async readAloud(turns: ChatTurn[], assistantTurnIndex: number, threadId: string | null, onCost: (cost: number) => void) {
		if (this.speakingIndex === assistantTurnIndex) {
			this.stop();
			return;
		}

		const turn = turns[assistantTurnIndex];
		if (!turn || turn.role !== 'assistant' || !turn.content) return;

		this.stop(); // only one read-aloud plays at a time
		this.speakingIndex = assistantTurnIndex;

		const result = await synthesize(turn.content, threadId ?? undefined);
		if (!result) {
			if (this.speakingIndex === assistantTurnIndex) this.speakingIndex = null;
			return;
		}
		// Stopped (or a different turn started) while we were still fetching.
		if (this.speakingIndex !== assistantTurnIndex) return;

		if (result.cost) onCost(result.cost);
		this.currentAudio = result.audio;
		result.audio.onended = () => {
			if (this.currentAudio === result.audio) {
				this.currentAudio = null;
				this.isPlaying = false;
				this.speakingIndex = null;
			}
		};

		try {
			await result.audio.play();
			this.isPlaying = true;
		} catch (err) {
			console.error('audio playback failed', err);
			this.speakingIndex = null;
		}
	}

	stop() {
		if (this.currentAudio) {
			this.currentAudio.onended = null;
			this.currentAudio.pause();
			this.currentAudio = null;
		}
		this.isPlaying = false;
		this.speakingIndex = null;
	}
}
