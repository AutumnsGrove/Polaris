<script lang="ts">
	import { appState } from '$lib/state.svelte';

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
			<label class="switch">
				<input
					type="checkbox"
					checked={isEnabled(tool.name)}
					onchange={(e) => appState.settings.setToolEnabled(tool.name, e.currentTarget.checked)}
				/>
				<span class="slider"></span>
			</label>
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

	/* Same switch construction as ComposerMenu.svelte/SettingsPanel.svelte —
	   duplicated rather than shared since Svelte scopes component styles
	   per-file, but it's the same visual vocabulary everywhere a boolean
	   setting appears. */
	.switch {
		position: relative;
		display: inline-block;
		width: 36px;
		height: 20px;
		flex-shrink: 0;
	}

	.switch input {
		opacity: 0;
		width: 0;
		height: 0;
	}

	.slider {
		position: absolute;
		inset: 0;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-full);
		cursor: pointer;
		transition: background 0.15s ease;
	}

	.slider::before {
		content: '';
		position: absolute;
		width: 14px;
		height: 14px;
		left: 2px;
		top: 2px;
		background: var(--color-text-dim);
		border-radius: 50%;
		transition:
			transform 0.15s ease,
			background 0.15s ease;
	}

	.switch input:checked + .slider {
		background: color-mix(in srgb, var(--color-accent) 30%, transparent);
		border-color: var(--color-accent);
	}

	.switch input:checked + .slider::before {
		transform: translateX(16px);
		background: var(--color-accent);
	}
</style>
