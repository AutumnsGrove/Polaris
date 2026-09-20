import { describe, it, expect, vi, beforeEach, type Mock } from 'vitest';
import { AudioPlayer } from './audio.svelte';
import type { ChatTurn } from './types';

vi.mock('./speech', () => ({ synthesizeStream: vi.fn() }));
import { synthesizeStream } from './speech';

describe('AudioPlayer.readAloud', () => {
	let player: AudioPlayer;
	let turns: ChatTurn[];
	let onCost: Mock<(cost: number) => void>;

	beforeEach(() => {
		player = new AudioPlayer();
		turns = [
			{ role: 'user', content: 'question' },
			{ role: 'assistant', content: 'the answer', id: 42 }
		];
		onCost = vi.fn<(cost: number) => void>();
		vi.mocked(synthesizeStream).mockReset();
	});

	it('sets speakingIndex synchronously, clears it and sets the persisted file once synthesis resolves', async () => {
		vi.mocked(synthesizeStream).mockResolvedValue({ cost: 0.001, file: '/api/workspace/t1/a.wav' });

		const promise = player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBe(1); // set synchronously, before the await resolves
		await promise;

		expect(player.speakingIndex).toBeNull();
		expect(turns[1].ttsAudioFile).toBe('/api/workspace/t1/a.wav');
		expect(player.justFinishedIndex).toBe(1);
		expect(onCost).toHaveBeenCalledWith(0.001);
		expect(turns[1].costUsd).toBe(0.001);
	});

	it('passes the turn id and thread id through to synthesizeStream', async () => {
		vi.mocked(synthesizeStream).mockResolvedValue({ cost: 0 });
		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(synthesizeStream).toHaveBeenCalledWith('the answer', 'thread-1', 42);
	});

	it('clicking the already-synthesizing turn cancels it instead of restarting', async () => {
		let resolveFirst!: (v: { cost: number }) => void;
		vi.mocked(synthesizeStream).mockImplementation(
			() => new Promise((resolve) => (resolveFirst = resolve))
		);

		const firstCall = player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBe(1);

		await player.readAloud(turns, 1, 'thread-1', onCost); // second click: stop()
		expect(player.speakingIndex).toBeNull();

		resolveFirst({ cost: 999 }); // the cancelled request resolving late must not resurrect state
		await firstCall;
		expect(player.speakingIndex).toBeNull();
		expect(onCost).not.toHaveBeenCalled();
	});

	it('does nothing for a non-assistant or empty turn', async () => {
		await player.readAloud(turns, 0, 'thread-1', onCost); // index 0 is the user turn
		expect(synthesizeStream).not.toHaveBeenCalled();
		expect(player.speakingIndex).toBeNull();
	});

	it('does nothing if the turn already has a persisted audio file', async () => {
		turns[1].ttsAudioFile = '/api/workspace/t1/a.wav';
		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(synthesizeStream).not.toHaveBeenCalled();
	});

	it('clears speakingIndex and skips the file/cost updates on a synthesis error', async () => {
		vi.mocked(synthesizeStream).mockResolvedValue({ cost: 0, error: 'boom' });
		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBeNull();
		expect(onCost).not.toHaveBeenCalled();
		expect(turns[1].ttsAudioFile).toBeUndefined();
	});
});

describe('AudioPlayer.stop', () => {
	it('is a no-op with nothing in flight', () => {
		const player = new AudioPlayer();
		expect(() => player.stop()).not.toThrow();
	});
});
