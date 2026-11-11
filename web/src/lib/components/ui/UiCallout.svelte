<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	// `loc` is the block's verification address prefix ("<fence>.<block>"); each
	// field below adds ".<item>.<field>", the names uiblocks/sites.go uses.
	let { block, loc }: { block: Extract<UiBlock, { kind: 'callout' }>; loc?: string } = $props();

	const ICON = { note: 'i', warn: '!', ok: '✓', answer: '✦' } as const;

	// "2026-10" -> "Oct 2026". UTC so a viewer's timezone can't shift the month.
	let asof = $derived(
		block.asof
			? new Date(`${block.asof}-01T00:00:00Z`).toLocaleDateString('en-US', {
					month: 'short',
					year: 'numeric',
					timeZone: 'UTC'
				})
			: ''
	);
</script>

<div class="ui-callout {block.tone}" role="note">
	<span class="ic" aria-hidden="true">{ICON[block.tone]}</span>
	<div class="body">
		<UiText text={block.text} loc="{loc}.0.text" />
		<UiSources src={block.src} loc="{loc}.0.src" />
		{#if asof}<span class="asof">as of {asof}</span>{/if}
	</div>
</div>

<style>
	.ui-callout {
		--tone: var(--color-accent-2);
		--tone-soft: var(--color-accent-2-soft);
		display: flex;
		gap: var(--space-md);
		align-items: flex-start;
		margin: var(--space-md) 0;
		padding: var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
		font-size: 14px;
	}

	.warn {
		--tone: var(--color-accent);
		--tone-soft: var(--color-accent-soft);
	}

	/* No dedicated "ok" token exists; the syntax palette's addition green is
	   the app's one existing success-green. */
	.ok {
		--tone: var(--color-syntax-addition);
		--tone-soft: color-mix(in srgb, var(--color-syntax-addition) 14%, transparent);
	}

	/* The bottom-line-up-front card: tinted border, always dated. */
	.answer {
		--tone: var(--color-accent);
		--tone-soft: var(--color-accent-soft);
		border-color: color-mix(in srgb, var(--color-accent) 45%, var(--color-border));
	}

	.ic {
		flex: none;
		display: grid;
		place-items: center;
		width: 26px;
		height: 26px;
		border-radius: var(--radius-full);
		background: var(--tone-soft);
		color: var(--tone);
		font-weight: 700;
		font-size: 14px;
	}

	.body {
		min-width: 0;
	}

	.asof {
		display: inline-block;
		margin-left: var(--space-sm);
		padding: 0 var(--space-sm);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-full);
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-dim);
		white-space: nowrap;
	}
</style>
