<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { globalMemorySource } from '$lib/memorySource';
	import MemoryManager from './MemoryManager.svelte';

	// Owned by SettingsPanel, not this component — "Import" opens its own
	// sibling subpage (MemoryImport.svelte) one level down from Memory,
	// same flat "showX" nav SettingsPanel already uses for Stats/Memory/
	// Tools, just one button deeper instead of a true nested route.
	let { onImport }: { onImport: () => void } = $props();
</script>

<MemoryManager
	source={globalMemorySource(appState.settings)}
	subject="about you"
	placeholder="e.g. I prefer metric units, or I'm a backend engineer"
	emptyText="Nothing saved yet — it'll remember things worth carrying forward as you talk."
	{onImport}
/>
