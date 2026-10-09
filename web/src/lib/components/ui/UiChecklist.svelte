<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';

	let { block }: { block: Extract<UiBlock, { kind: 'checklist' }> } = $props();

	// Ticks are local state, keyed by item position. The block is re-parsed on
	// every streamed token but this component instance is kept (UiBlocks keys
	// by index), so a tick survives later items arriving. Never persisted:
	// decision 1 is static + local toggles only.
	let done = $state<boolean[]>([]);

	let count = $derived(done.slice(0, block.items.length).filter(Boolean).length);
</script>

<div class="ui-checklist">
	{#if block.title}<div class="title">{block.title}</div>{/if}
	<!-- div + role, not ul/li: `.prose ul { padding-left }` would indent it. -->
	<div role="list">
		{#each block.items as item, i (i)}
			<div role="listitem">
				<label>
					<input type="checkbox" bind:checked={done[i]} />
					<span class:done={done[i]}><UiText text={item} /></span>
				</label>
			</div>
		{/each}
	</div>
	{#if block.items.length > 1}
		<div class="progress" aria-label="{count} of {block.items.length} done">
			<div class="bar"><i style="width: {(count / block.items.length) * 100}%"></i></div>
			<span>{count}/{block.items.length}</span>
		</div>
	{/if}
</div>

<style>
	.ui-checklist {
		margin: var(--space-md) 0;
		padding: var(--space-md) var(--space-lg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
	}

	.title {
		margin-bottom: var(--space-sm);
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-dim);
	}

	/* Whole row is the tap target: this is read on a phone. */
	label {
		display: flex;
		gap: var(--space-md);
		align-items: flex-start;
		padding: var(--space-xs) 0;
		font-size: 14px;
		cursor: pointer;
	}

	input {
		flex: none;
		margin-top: 3px;
		accent-color: var(--color-accent);
	}

	span {
		min-width: 0;
		overflow-wrap: anywhere;
	}

	span.done {
		color: var(--color-text-dim);
		text-decoration: line-through;
	}

	.progress {
		display: flex;
		gap: var(--space-md);
		align-items: center;
		margin-top: var(--space-sm);
		font-family: var(--font-mono);
		font-size: 11.5px;
		color: var(--color-text-dim);
	}

	.bar {
		flex: 1;
		height: 4px;
		border-radius: var(--radius-full);
		background: var(--color-border);
		overflow: hidden;
	}

	.bar i {
		display: block;
		height: 100%;
		background: var(--color-accent);
		transition: width 0.2s;
	}

	@media (prefers-reduced-motion: reduce) {
		.bar i {
			transition: none;
		}
	}
</style>
