<script lang="ts">
	import type { Component } from 'svelte';

	// Oracle's offer lines (docs/plans/oracle-mode.md's 7a) under an answer, one
	// row per chip. busy is the key of the offer mid-flight, if any.
	type Offer = {
		key: string;
		label?: string;
		fieldId?: string;
		meta: { icon: Component<any>; verb: string; label: (l?: string) => string };
	};
	let {
		offers,
		busy,
		onActivate
	}: {
		offers: Offer[];
		busy: string | null;
		onActivate: (key: string, fieldId?: string, label?: string) => void;
	} = $props();
</script>

<div class="offer-lines">
	{#each offers as offer (offer.key)}
		<button
			class="offer-line"
			type="button"
			disabled={busy !== null}
			onclick={() => onActivate(offer.key, offer.fieldId, offer.label)}
		>
			<offer.meta.icon size={14} />
			<span>{@html offer.meta.label(offer.label)}</span>
			<span class="go">{busy === offer.key ? (offer.key === 'field' ? 'Moving…' : 'Writing…') : offer.meta.verb}</span>
		</button>
	{/each}
</div>

<style>
	/* 7a offer lines (docs/plans/oracle-mode.md) — one row per Oracle chip,
	   ported from mockups/oracle-mode.html's .offer-lines/.offer-line. */
	.offer-lines {
		display: flex;
		flex-direction: column;
		margin-top: var(--space-sm);
		border-top: 1px solid var(--color-border);
	}

	.offer-line {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		width: 100%;
		border: none;
		background: none;
		padding: var(--space-sm) 0;
		border-bottom: 1px solid var(--color-border);
		font-size: 13px;
		color: var(--color-text-dim);
		text-align: left;
		cursor: pointer;
	}

	.offer-line:disabled {
		cursor: default;
		opacity: 0.6;
	}

	.offer-line :global(svg) {
		flex-shrink: 0;
		color: var(--color-accent);
	}

	.offer-line :global(b) {
		color: var(--color-text);
		font-weight: 500;
	}

	.offer-line .go {
		margin-left: auto;
		flex-shrink: 0;
		color: var(--color-accent);
		font-size: 12.5px;
		font-weight: 500;
	}
</style>
