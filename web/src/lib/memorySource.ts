import type { Memory, SettingsState } from './settings.svelte';

// What MemoryManager.svelte and MemoryImport.svelte need from whichever
// memory store they are showing — the global list in Settings, or one Field's
// own store (issue #133). An interface rather than a base class so Settings
// can adapt the existing appState.settings fields (which other code also
// reads) without moving them.
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

	// "Bring memories from another AI": one request parses the pasted dump
	// into this store. Resolves true on success (the caller clears its
	// textarea only then, so a failed import doesn't lose a long paste).
	readonly importBusy: boolean;
	readonly importMessage: string;
	importMemories(dump: string): Promise<boolean>;
	// Where the plain-text download lives.
	readonly exportHref: string;
}

// The global memory list as a MemorySource. Getters (not copies) so it stays
// reactive to SettingsState, which the rest of the panel also reads/writes.
export function globalMemorySource(settings: SettingsState): MemorySource {
	return {
		get memories() {
			return settings.memories;
		},
		get loaded() {
			return settings.memoriesLoaded;
		},
		get busy() {
			return settings.memoryChatBusy;
		},
		get message() {
			return settings.memoryChatMessage;
		},
		get importBusy() {
			return settings.importBusy;
		},
		get importMessage() {
			return settings.importMessage;
		},
		exportHref: '/api/memories/export',
		load: () => settings.loadMemories(),
		remove: (name) => settings.deleteMemory(name),
		instruct: (text) => settings.sendMemoryInstruction(text),
		importMemories: (dump) => settings.importMemories(dump)
	};
}
