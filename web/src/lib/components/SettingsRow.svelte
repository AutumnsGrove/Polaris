<script lang="ts">
	import type { Component, Snippet } from 'svelte';
	import { ChevronRight, Info } from '@lucide/svelte';

	// The one row every settings surface is built from: icon tile, title,
	// at most one dim line of description, an optional control on the right,
	// optional full-width content below (a textarea, a segmented control, the
	// voice grid), and an optional "Details" disclosure for anything longer
	// than a line. Svelte scopes CSS per component, which is what used to let
	// each Settings section grow its own spacing and hint treatment — this
	// keeps the anatomy defined once. Rows are meant to be direct children of
	// a card (.settings-group in SettingsPanel), which supplies the border and
	// radius; the divider between rows lives here.
	//
	// Pass `onclick` to make the whole row a button (opens a sub-view, or
	// fires an action); `chevron={false}` for an action rather than a
	// drill-in, since a chevron promises navigation.
	let {
		icon,
		title,
		desc,
		control,
		children,
		details,
		onclick,
		chevron = true,
		disabled = false,
		spin = false
	}: {
		icon?: Component<{ size?: number; class?: string }>;
		title: string;
		desc?: string;
		control?: Snippet;
		children?: Snippet;
		details?: Snippet;
		onclick?: () => void;
		chevron?: boolean;
		disabled?: boolean;
		spin?: boolean;
	} = $props();

	const Icon = $derived(icon);

	// "Polaris" is the product name and always renders in the reserved
	// wordmark face (same treatment as the sidebar and welcome heading), so
	// titles and descriptions can stay plain strings at the call site.
	function parts(text: string): { text: string; wordmark: boolean }[] {
		return text
			.split(/(Polaris)/)
			.filter((t) => t !== '')
			.map((t) => ({ text: t, wordmark: t === 'Polaris' }));
	}
</script>

{#snippet label()}
	{#if Icon}
		<span class="lead" aria-hidden="true"><Icon size={16} class={spin ? 'spin' : ''} /></span>
	{:else}
		<span class="lead-spacer" aria-hidden="true"></span>
	{/if}
	<span class="text">
		<span class="title">
			{#each parts(title) as p, i (i)}{#if p.wordmark}<span class="wordmark">{p.text}</span>{:else}{p.text}{/if}{/each}
		</span>
		{#if desc}
			<span class="desc">
				{#each parts(desc) as p, i (i)}{#if p.wordmark}<span class="wordmark">{p.text}</span>{:else}{p.text}{/if}{/each}
			</span>
		{/if}
	</span>
{/snippet}

<div class="row-wrap" class:disabled>
	<div class="row" class:has-below={!!children}>
		{#if onclick}
			<button type="button" class="main main-button" {onclick} {disabled}>
				{@render label()}
				{#if chevron}<ChevronRight size={16} class="chev" />{/if}
			</button>
		{:else}
			<div class="main">
				{@render label()}
			</div>
		{/if}
		{#if control}
			<div class="control">{@render control()}</div>
		{/if}
	</div>

	{#if children}
		<div class="below">{@render children()}</div>
	{/if}

	{#if details}
		<details class="more">
			<summary><Info size={13} /> Details</summary>
			<div class="more-body">{@render details()}</div>
		</details>
	{/if}
</div>

<style>
	.row-wrap {
		border-top: 1px solid var(--color-border);
	}

	.row-wrap:first-child {
		border-top: none;
	}

	/* Same blocked-and-dimmed treatment the old per-section wrapper used
	   (Memory's "What Polaris remembers" once Memory is off). pointer-events
	   is the real block for anything without its own disabled attribute. */
	.row-wrap.disabled {
		opacity: 0.45;
		pointer-events: none;
	}

	.row {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-lg);
		min-height: 56px;
	}

	.row.has-below {
		padding-bottom: var(--space-sm);
	}

	.main {
		flex: 1;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: var(--space-md);
	}

	/* A button row sits flush to the card edge so the whole strip is the tap
	   target, not just the text. */
	.row:has(.main-button) {
		padding: 0;
	}

	.main-button {
		border: none;
		background: transparent;
		color: inherit;
		font: inherit;
		text-align: left;
		padding: var(--space-md) var(--space-lg);
		min-height: 56px;
		cursor: pointer;
	}

	.main-button:hover:not(:disabled) {
		background: var(--color-surface-3);
	}

	.row:has(.main-button) .control {
		padding-right: var(--space-lg);
	}

	.lead {
		display: grid;
		place-items: center;
		width: 32px;
		height: 32px;
		flex-shrink: 0;
		border-radius: var(--radius-md);
		background: var(--color-accent-soft);
		color: var(--color-accent);
	}

	/* Keeps a row's text aligned with its siblings' when it has no icon of
	   its own. */
	.lead-spacer {
		width: 32px;
		flex-shrink: 0;
	}

	.text {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.title {
		font-size: 14px;
		overflow-wrap: anywhere;
		font-weight: 500;
		line-height: 1.3;
	}

	.desc {
		font-size: 12px;
		line-height: 1.4;
		color: var(--color-text-dim);
	}

	.control {
		display: flex;
		align-items: center;
		flex-shrink: 0;
		max-width: 60%;
	}

	.main-button :global(.chev) {
		margin-left: auto;
		flex-shrink: 0;
		color: var(--color-text-dim);
	}

	.below {
		padding: 0 var(--space-lg) var(--space-md);
	}

	.more {
		border-top: 1px solid var(--color-border);
	}

	.more summary {
		list-style: none;
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		padding: var(--space-sm) var(--space-lg);
		font-size: 12px;
		color: var(--color-text-dim);
		cursor: pointer;
	}

	.more summary::-webkit-details-marker {
		display: none;
	}

	.more summary:hover,
	.more[open] summary {
		color: var(--color-accent);
	}

	.more-body {
		padding: 0 var(--space-lg) var(--space-md);
		font-size: 12px;
		line-height: 1.55;
		color: var(--color-text-dim);
	}

	.more-body :global(code) {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 11px;
	}

	.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		font-size: 1.05em;
		letter-spacing: 0.02em;
	}
</style>
