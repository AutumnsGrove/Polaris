import type { ChatTurn } from './types';
import { synthesizeStream } from './speech';

// Manual per-message read-aloud, split out of state.svelte.ts since it's a
// self-contained concern (one async action + its loading state) with
// exactly one consumer (ChatTurnView's speaker icon). speakingIndex is set
// for the duration of the synthesis request only — this class no longer
// owns any playback itself; once the full answer is synthesized and
// persisted, WaveformAudioPlayer (a real component with its own <audio>
// element and scrub UI) takes over entirely.
//
// This used to play each sentence-chunk live as it streamed in, queuing
// ephemeral blob-URL Audio elements one after another — the original
// rationale being faster time-to-first-audio on a long answer. Live
// testing found that queue silently breaking after the first chunk more
// often than not, and separately, Kokoro synthesizing a typical answer
// end-to-end only takes a few seconds anyway — not enough latency to be
// worth chasing that bug for. Simpler and more robust to just wait for the
// one real, persisted, reloadable file and hand playback to the one
// visible widget the user can actually see and scrub, instead of an
// invisible queue playing in the background that's decoupled from it.
export class AudioPlayer {
	speakingIndex = $state<number | null>(null);
	// Set to the turn index whose synthesis just finished THIS click (not
	// one whose audio was already persisted from history) — the one-shot
	// signal ChatTurnView passes to WaveformAudioPlayer's `autoplay` prop.
	// Never reset back to null: a fresh WaveformAudioPlayer instance only
	// ever mounts once per turn, at the exact moment turn.ttsAudioFile
	// transitions from unset to set, so the mount-time check this drives
	// only ever fires for that one live-finishing turn, never a reload.
	justFinishedIndex = $state<number | null>(null);

	// True once a silent clip has successfully played inside a real user
	// gesture this tab session — see unlock()'s doc comment.
	private unlocked = false;
	// Bumped on every stop()/new readAloud call so a synthesis request
	// that resolves after being superseded recognizes it's stale and
	// doesn't clobber whatever's happening now.
	private sessionToken = 0;

	// Browsers grant continued playback permission for the rest of a tab's
	// lifetime once a media element has successfully played following a
	// direct user gesture (Chrome's autoplay policy and Safari's
	// equivalent). Both this request's own synthesis wait AND
	// WaveformAudioPlayer's later autoplay happen well after this click's
	// activation window would otherwise have expired, especially on iOS
	// Safari in this app's `display: standalone` PWA mode — which is what
	// made read-aloud never audibly work in the first place (see
	// docs/plans/voice-playback-infra.md's Fix 1). Playing (and
	// immediately pausing) a trivial silent clip synchronously inside the
	// click handler, before any await, is the standard workaround: it
	// satisfies the "played during a real gesture" requirement once, and
	// every later async .play() in this tab session — including the
	// widget's own autoplay — stops being blocked.
	private unlock() {
		if (this.unlocked) return;
		this.unlocked = true;
		const silence = new Audio(
			'data:audio/wav;base64,UklGRiYAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQIAAAAAAA=='
		);
		silence
			.play()
			.then(() => silence.pause())
			.catch(() => {
				// If even this fails, the widget's real playback will too —
				// nothing more to do here, the failure surfaces there instead.
			});
	}

	// Clicking the turn that's already synthesizing cancels the request —
	// a toggle, not just a one-way trigger. onCost reports the synthesis's
	// billed cost back to the caller (folded into the thread's running
	// total) since this class has no thread state of its own.
	async readAloud(turns: ChatTurn[], assistantTurnIndex: number, threadId: string | null, onCost: (cost: number) => void) {
		if (this.speakingIndex === assistantTurnIndex) {
			this.stop();
			return;
		}

		const turn = turns[assistantTurnIndex];
		if (!turn || turn.role !== 'assistant' || !turn.content || turn.ttsAudioFile) return;

		// Must run synchronously, before any await below — see unlock()'s
		// doc comment on why the gesture window closes otherwise.
		this.unlock();

		this.stop(); // only one synthesis request in flight at a time
		const token = ++this.sessionToken;
		this.speakingIndex = assistantTurnIndex;

		const result = await synthesizeStream(turn.content, threadId ?? undefined, turn.id);

		if (token !== this.sessionToken) return; // stopped/superseded meanwhile
		this.speakingIndex = null;

		if (result.cost) {
			onCost(result.cost); // bumps appState.totalCost, the thread-wide running total
			// Also fold onto the turn's own visible cost badge — the figure
			// shown right next to this same read-aloud button/player in
			// ChatTurnView, which otherwise never reflected read-aloud's real
			// spend even though totalCost (shown only in ThreadMenu) did.
			turn.costUsd = (turn.costUsd ?? 0) + result.cost;
		}
		if (result.error) {
			console.error('TTS stream ended early', result.error);
		}
		// turn is the same reactive object live in appState.turns (passed by
		// reference, not copied) — setting this here is what makes
		// WaveformAudioPlayer mount and autoplay the instant this session's
		// synthesis finishes, without waiting for a reload.
		if (result.file) {
			turn.ttsAudioFile = result.file;
			this.justFinishedIndex = assistantTurnIndex;
		}
	}

	stop() {
		this.sessionToken++; // any in-flight synthesizeStream call becomes stale
		this.speakingIndex = null;
	}
}
