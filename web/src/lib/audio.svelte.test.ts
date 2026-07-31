import { describe, it, expect, vi, beforeEach, type Mock } from 'vitest';
import { AudioPlayer } from './audio.svelte';
import type { ChatTurn } from './types';

vi.mock('./speech', () => ({ synthesizeStream: vi.fn() }));
import { synthesizeStream } from './speech';

function fakeAudio() {
	return {
		src: 'blob:fake-url',
		play: vi.fn().mockResolvedValue(undefined),
		pause: vi.fn(),
		onended: null as (() => void) | null
	};
}

// Simulates synthesizeStream's real behavior: calls onChunk for each fake
// audio element in order, then resolves with the given result.
function streamOf(chunks: ReturnType<typeof fakeAudio>[], result: { cost: number; error?: string } = { cost: 0 }) {
	return vi.fn().mockImplementation(async (_text: string, _threadId: string | undefined, onChunk: (a: any) => void) => {
		for (const c of chunks) onChunk(c);
		return result;
	});
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
		vi.mocked(synthesizeStream).mockReset();
	});

	it('sets speakingIndex immediately, then isPlaying once the first chunk starts', async () => {
		const chunk = fakeAudio();
		vi.mocked(synthesizeStream).mockImplementation(streamOf([chunk], { cost: 0.001 }));

		const promise = player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBe(1); // set synchronously, before the await resolves
		await promise;

		expect(player.isPlaying).toBe(true);
		expect(chunk.play).toHaveBeenCalledOnce();
		expect(onCost).toHaveBeenCalledWith(0.001);
	});

	it('plays multiple chunks in order, one after another', async () => {
		const first = fakeAudio();
		const second = fakeAudio();
		vi.mocked(synthesizeStream).mockImplementation(streamOf([first, second], { cost: 0.002 }));

		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(first.play).toHaveBeenCalledOnce();
		expect(second.play).not.toHaveBeenCalled();

		first.onended!();
		expect(second.play).toHaveBeenCalledOnce();
	});

	it('finishes the session only after the last chunk ends', async () => {
		const first = fakeAudio();
		const second = fakeAudio();
		vi.mocked(synthesizeStream).mockImplementation(streamOf([first, second], { cost: 0 }));

		await player.readAloud(turns, 1, 'thread-1', onCost);
		first.onended!();
		expect(player.speakingIndex).toBe(1); // still going — second chunk playing

		second.onended!();
		expect(player.isPlaying).toBe(false);
		expect(player.speakingIndex).toBeNull();
	});

	it('clicking the already-speaking turn stops it instead of restarting', async () => {
		const chunk = fakeAudio();
		vi.mocked(synthesizeStream).mockImplementation(streamOf([chunk], { cost: 0 }));
		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBe(1);

		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBeNull();
		expect(chunk.pause).toHaveBeenCalledOnce();
		// Only the one synthesizeStream call from the first invocation.
		expect(synthesizeStream).toHaveBeenCalledOnce();
	});

	it('does nothing for a non-assistant or empty turn', async () => {
		await player.readAloud(turns, 0, 'thread-1', onCost); // index 0 is the user turn
		expect(synthesizeStream).not.toHaveBeenCalled();
		expect(player.speakingIndex).toBeNull();
	});

	it('clears speakingIndex if no chunks ever synthesize', async () => {
		vi.mocked(synthesizeStream).mockImplementation(streamOf([], { cost: 0, error: 'boom' }));
		await player.readAloud(turns, 1, 'thread-1', onCost);
		expect(player.speakingIndex).toBeNull();
		expect(onCost).not.toHaveBeenCalled();
	});
});

describe('AudioPlayer.stop', () => {
	it('pauses current audio, drops queued chunks, and resets state', async () => {
		vi.mocked(synthesizeStream).mockReset();
		const first = fakeAudio();
		const second = fakeAudio();
		vi.mocked(synthesizeStream).mockImplementation(streamOf([first, second], { cost: 0 }));

		const player = new AudioPlayer();
		await player.readAloud([{ role: 'assistant', content: 'a' }], 0, null, vi.fn());
		player.stop();

		expect(first.pause).toHaveBeenCalledOnce();
		expect(player.isPlaying).toBe(false);
		expect(player.speakingIndex).toBeNull();
	});

	it('is a no-op with nothing playing', () => {
		const player = new AudioPlayer();
		expect(() => player.stop()).not.toThrow();
	});
});
