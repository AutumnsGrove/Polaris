<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import type { MemorySource } from '$lib/memorySource';
	import MemoryManager from './MemoryManager.svelte';

	// Owned by SettingsPanel, not this component — "Import" opens its own
	// sibling subpage (MemoryImport.svelte) one level down from Memory,
	// same flat "showX" nav SettingsPanel already uses for Stats/Memory/
	// Tools, just one button deeper instead of a true nested route.
	let { onImport }: { onImport: () => void } = $props();

	// The global memory list as a MemorySource. Getters (not copies) so the
	// shared list stays reactive to appState.settings, which MemoryImport and
	// the rest of the panel also read and write.
	const source: MemorySource = {
		get memories() {
			return appState.settings.memories;
		},
		get loaded() {
			return appState.settings.memoriesLoaded;
		},
		get busy() {
			return appState.settings.memoryChatBusy;
		},
		get message() {
			return appState.settings.memoryChatMessage;
		},
		load: () => appState.settings.loadMemories(),
		remove: (name) => appState.settings.deleteMemory(name),
		instruct: (text) => appState.settings.sendMemoryInstruction(text)
	};
</script>

<MemoryManager
	{source}
	subject="about you"
	placeholder="e.g. I prefer metric units, or I'm a backend engineer"
	emptyText="Nothing saved yet — it'll remember things worth carrying forward as you talk."
	{onImport}
	exportHref="/api/memories/export"
/>
