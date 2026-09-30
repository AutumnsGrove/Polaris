import type { Memory } from './settings.svelte';

// What MemoryManager.svelte needs from whichever memory store it is showing —
// the global list in Settings, or one Field's own store (issue #133). An
// interface rather than a base class so Settings can adapt the existing
// appState.settings fields (which other code, e.g. memory import, also reads)
// without moving them.
export interface MemorySource {
	readonly memories: Memory[];
	// False until the first load finishes, so the list can say "Loading…"
	// instead of flashing its empty state.
	readonly loaded: boolean;
	// True while a "tell it what to change" instruction is being processed.
	readonly busy: boolean;
	// The last instruction's short confirmation ('' when none).
	readonly message: string;
	load(): Promise<void>;
	remove(name: string): Promise<boolean>;
	instruct(instruction: string): Promise<void>;
}
