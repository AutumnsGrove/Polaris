<script lang="ts">
	import type { ClaimVerdict, UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	let { block }: { block: Extract<UiBlock, { kind: 'claim' }> } = $props();

	// A word and a glyph, never colour alone. The pill is the model's own read
	// of the evidence, NOT a verified result (decision 17), so it deliberately
	// has no caption and never carries a "found in source" tick: only the
	// evidence lines' source chips do.
	const VERDICT: Record<ClaimVerdict, { label: string; mark: string }> = {
		true: { label: 'True', mark: '✓' },
		mixed: { label: 'Mixed', mark: '~' },
		misleading: { label: 'Misleading', mark: '!' },
		false: { label: 'False', mark: '✕' },
		unverified: { label: 'Unverified', mark: '?' }
	};
	let v = $derived(VERDICT[block.verdict]);
</script>

<div class="ui-claim">
	<div class="head">
		<span class="pill {block.verdict}"><span aria-hidden="true">{v.mark}</span> {v.label}</span>
	</div>
	<div class="text"><UiText text={block.text} /></div>

	{#if block.supports.length}
		<div class="side supports">
			<h4>Supports</h4>
			<div role="list">
				{#each block.supports as e, i (i)}
					<div class="row" role="listitem"><b aria-hidden="true">+</b><span><UiText text={e.text} /><UiSources src={e.src} /></span></div>
				{/each}
			</div>
		</div>
	{/if}
	{#if block.disputes.length}
		<div class="side disputes">
			<h4>Disputes</h4>
			<div role="list">
				{#each block.disputes as e, i (i)}
					<div class="row" role="listitem"><b aria-hidden="true">–</b><span><UiText text={e.text} /><UiSources src={e.src} /></span></div>
				{/each}
			</div>
		</div>
	{/if}
</div>

<style>
	.ui-claim {
		margin: var(--space-md) 0;
		padding: var(--space-md) var(--space-lg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
	}

	.pill {
		display: inline-block;
		padding: 0 var(--space-md);
		border: 1px solid var(--tone);
		border-radius: var(--radius-full);
		color: var(--tone);
		font-size: 11.5px;
		font-weight: 700;
		letter-spacing: 0.05em;
		text-transform: uppercase;
	}

	.true {
		--tone: var(--color-syntax-addition);
	}

	.mixed,
	.misleading {
		--tone: var(--color-accent);
	}

	.false {
		--tone: var(--color-danger);
	}

	.unverified {
		--tone: var(--color-text-dim);
	}

	.text {
		margin: var(--space-sm) 0 var(--space-xs);
		font-family: var(--font-serif);
		font-size: 17px;
		line-height: 1.4;
		overflow-wrap: break-word;
	}

	.side {
		margin-top: var(--space-sm);
	}

	h4 {
		margin: 0 0 var(--space-xs);
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-dim);
	}

	.row {
		display: flex;
		gap: var(--space-sm);
		padding: 2px 0;
		font-size: 13.5px;
	}

	b {
		flex: none;
		width: 1ch;
		font-weight: 700;
	}

	.supports b {
		color: var(--color-syntax-addition);
	}

	.disputes b {
		color: var(--color-danger);
	}

	.row span {
		min-width: 0;
		overflow-wrap: break-word;
	}
</style>
