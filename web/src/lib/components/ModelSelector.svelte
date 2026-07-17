<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { Cpu } from '@lucide/svelte';
</script>

<label class="model-selector">
	<Cpu size={14} color="var(--color-text-dim)" />
	<select bind:value={appState.selectedModel}>
		{#each appState.models as model (model.id)}
			<option value={model.id}>{model.name}</option>
		{/each}
	</select>
</label>

<style>
	.model-selector {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		border: 1px solid var(--color-border);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: 4px 10px;
	}

	select {
		border: none;
		background: transparent;
		font-size: 13px;
		outline: none;
	}

	select option {
		background: var(--color-surface-2);
	}
</style>
