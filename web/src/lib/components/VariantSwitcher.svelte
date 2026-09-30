<script lang="ts">
	import { ChevronLeft, ChevronRight } from '@lucide/svelte';

	// The "‹ 2/3 ›" switcher for an assistant reply that has been edited or
	// regenerated. position is 0-based; onBrowse(±1) moves between variants.
	let { position, total, onBrowse }: { position: number; total: number; onBrowse: (delta: number) => void } = $props();
</script>

<div class="variant-switcher">
	<button
		class="icon-btn"
		onclick={() => onBrowse(-1)}
		disabled={position <= 0}
		title="Previous response"
	>
		<ChevronLeft size={13} />
	</button>
	<span class="variant-position">{position + 1}/{total}</span>
	<button
		class="icon-btn"
		onclick={() => onBrowse(1)}
		disabled={position >= total - 1}
		title="Next response"
	>
		<ChevronRight size={13} />
	</button>
</div>

<style>
	/* Leads the footer, not tucked in with the utility icons — browsing
	   past replies is a real navigation action, not a minor aside like
	   copy/read-aloud. The position readout sits in a shallow well (same
	   "carved, not drawn" language as inputs/readouts elsewhere) between
	   its two arrows so it reads as one compact control. */
	.variant-switcher {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		margin-right: var(--space-sm);
	}

	.variant-position {
		min-width: 28px;
		padding: var(--space-xs) var(--space-xs);
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-well);
		text-align: center;
		font-size: 11px;
		color: var(--color-text-dim);
		font-variant-numeric: tabular-nums;
	}
</style>
