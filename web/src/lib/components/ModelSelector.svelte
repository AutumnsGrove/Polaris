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
		border-radius: 999px;
		padding: 4px 10px 4px 12px;
		transition: border-color 0.15s var(--ease-out-expo), background-color 0.15s var(--ease-out-expo);
	}

	.model-selector:hover {
		border-color: var(--color-border-strong);
		background: var(--color-surface-3);
	}

	.model-selector:focus-within {
		border-color: var(--color-accent-2);
	}

	select {
		border: none;
		background: transparent;
		font-size: 13px;
		font-family: var(--font-sans);
		color: var(--color-text);
		outline: none;
		cursor: pointer;
		padding: 2px 0;
	}

	select option {
		background: var(--color-surface);
		color: var(--color-text);
	}

	/* A long model name ("DeepSeek V4 Flash") can otherwise push the
	   context/cost chips clean off a phone-width header. */
	@media (max-width: 480px) {
		select {
			max-width: 92px;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}
	}
</style>
