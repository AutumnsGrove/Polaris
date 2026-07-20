import { describe, it, expect, vi, beforeEach, type Mock } from 'vitest';
import { AudioPlayer } from './audio.svelte';
import type { ChatTurn } from './types';

vi.mock('./speech', () => ({ synthesize: vi.fn() }));
import { synthesize } from './speech';

function fakeAudio() {
	return {
		play: vi.fn().mockResolvedValue(undefined),
		pause: vi.fn(),
		onended: null as (() => void) | null
	};
}

describe('AudioPlayer.readAloud', () => {
	let player: AudioPlayer;
	let turns: ChatTurn[];
	let onCost: Mock<(cost: number) => void>;

	beforeEach(() => {
		player = new AudioPlayer();
		turns = [
			{ role: 'user', content: 'question' },
			{ role: 'assistant', content: 'the answer' }
		];
		onCost = vi.fn<(cost: number) => void>();
		vi.mocked(synthesize).mockReset();
	});

	it('sets speakingIndex immediately, then isPlaying once audio starts', async () => {
		const audio = fakeAudio();
		vi.mocked(synthesize).mockResolvedValue({ audio: audio as any, cost: 0.001 });

		const promise = player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBe(1); // set synchronously, before the await resolves
		await promise;

		expect(player.isPlaying).toBe(true);
		expect(audio.play).toHaveBeenCalledOnce();
		expect(onCost).toHaveBeenCalledWith(0.001);
	});

	it('clicking the already-speaking turn stops it instead of restarting', async () => {
		const audio = fakeAudio();
		vi.mocked(synthesize).mockResolvedValue({ audio: audio as any, cost: 0 });
		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBe(1);

		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBeNull();
		expect(audio.pause).toHaveBeenCalledOnce();
		// Only the one synthesize call from the first invocation.
		expect(synthesize).toHaveBeenCalledOnce();
	});

	it('does nothing for a non-assistant or empty turn', async () => {
		await player.readAloud(turns, 0, 'thread-1', onCost); // index 0 is the user turn
		expect(synthesize).not.toHaveBeenCalled();
		expect(player.speakingIndex).toBeNull();
	});

	it('clears speakingIndex if synthesis fails', async () => {
		vi.mocked(synthesize).mockResolvedValue(null);
		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBeNull();
		expect(onCost).not.toHaveBeenCalled();
	});

	it('onended resets state once playback finishes', async () => {
		const audio = fakeAudio();
		vi.mocked(synthesize).mockResolvedValue({ audio: audio as any, cost: 0 });
		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(audio.onended).toBeInstanceOf(Function);

		audio.onended!();
		expect(player.isPlaying).toBe(false);
		expect(player.speakingIndex).toBeNull();
	});
});

describe('AudioPlayer.stop', () => {
	it('pauses current audio and resets state', async () => {
		vi.mocked(synthesize).mockReset();
		const audio = fakeAudio();
		vi.mocked(synthesize).mockResolvedValue({ audio: audio as any, cost: 0 });

		const player = new AudioPlayer();
		await player.readAloud([{ role: 'assistant', content: 'a' }], 0, null, vi.fn());
		player.stop();

		expect(audio.pause).toHaveBeenCalledOnce();
		expect(player.isPlaying).toBe(false);
		expect(player.speakingIndex).toBeNull();
	});

	it('is a no-op with nothing playing', () => {
		const player = new AudioPlayer();
		expect(() => player.stop()).not.toThrow();
	});
});
