<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import Switch from './Switch.svelte';

	// toggleableTools/disabledTools are loaded once at app startup (see
	// SettingsState.load(), fired from +layout.svelte's onMount) — unlike
	// MemorySettings' loadMemories(), there's no per-mount refetch here,
	// since this list isn't something anything else in the app can change
	// out from under it the way a memory could get added mid-conversation.
	function isEnabled(name: string): boolean {
		return !appState.settings.disabledTools.includes(name);
	}
</script>

<section class="tool-list">
	<p class="hint intro">
		Turn off individual tools you don't want the assistant reaching for — it'll just answer
		without them instead.
	</p>
	{#each appState.settings.toggleableTools as tool (tool.name)}
		<label class="tool-row">
			<span class="tool-text">
				<span class="tool-name">{tool.name}</span>
				<span class="tool-description">{tool.description}</span>
			</span>
			<Switch
				label={tool.name}
				checked={isEnabled(tool.name)}
				onchange={(v) => appState.settings.setToolEnabled(tool.name, v)}
			/>
		</label>
	{/each}
</section>

<style>
	.hint {
		font-size: 12px;
		color: var(--color-text-dim);
		margin: 0 0 var(--space-lg) 0;
	}

	.tool-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.tool-row {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: var(--space-sm) var(--space-md);
	}

	.tool-text {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}

	.tool-name {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text);
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
	}

	.tool-description {
		margin-top: 2px;
		font-size: 12px;
		color: var(--color-text-dim);
		line-height: 1.4;
	}

</style>
