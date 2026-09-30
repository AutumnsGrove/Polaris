<script lang="ts">
	import { Loader2 } from '@lucide/svelte';
	import { chipLabel, type ThinkingChip } from '$lib/transponderHelpers';

	// Transponder's Thinking screen: a glanceable list of what the model is
	// doing right now (tool calls and reasoning bursts), or a plain
	// "Thinking…" when nothing has started yet.
	let { chips }: { chips: ThinkingChip[] } = $props();

	let chipListEl: HTMLDivElement | undefined = $state();
	// Keeps the newest chip in view as more stream in — without this, once
	// the list is taller than its capped max-height, a fresh chip appends
	// below the fold and the "what's happening right now" signal this
	// screen exists for goes invisible again, just via a different
	// mechanism than the original unbounded-height bug.
	$effect(() => {
		chips.length;
		queueMicrotask(() => chipListEl?.scrollTo({ top: chipListEl.scrollHeight, behavior: 'smooth' }));
	});
</script>

{#if chips.length > 0}
	<div class="chip-list" bind:this={chipListEl}>
		{#each chips as item, i (i)}
			<div class="chip">
				{#if !item.done}
					<Loader2 size={11} color="var(--color-accent-2)" class="spin" />
				{/if}
				{chipLabel(item)}
			</div>
		{/each}
	</div>
{:else}
	<div class="thinking-copy">Thinking…</div>
{/if}

<style>
	.thinking-copy {
		font-size: 15px;
		color: var(--color-text-dim);
		font-style: italic;
	}

	.chip-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		max-width: 300px;
		/* Capped and internally scrollable, same reasoning as .reply-card/
		   .transcript-bubble — a research-heavy turn can run to a dozen+
		   web_search/web_read chips (live-caught, see the screenshot this
		   was reported from), which without a cap just kept growing and
		   crushed everything else in .stage regardless of the safe-center
		   fix. Auto-scrolls to the newest chip (see the chipListEl bind:this
		   + $effect above) so the live "what's happening right now" chip
		   is always the one visible, not buried above the fold. */
		max-height: 220px;
		overflow-y: auto;
	}

	.chip {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		color: var(--color-text-dim);
		background: var(--color-surface-3);
		border-radius: var(--radius-full);
		padding: 6px 10px;
		width: fit-content;
	}
</style>
