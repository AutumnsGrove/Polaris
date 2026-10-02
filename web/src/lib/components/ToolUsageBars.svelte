<script lang="ts">
	// Ranked per-tool call bars for the Usage panel (issue #150): bar length is
	// calls, the red slice is the errored share, and the right-hand column is
	// the error rate. Pure presentation over store.Stats' tool_call_counts /
	// tool_error_counts, which are already period-filtered server-side, so
	// there's no new endpoint behind it.
	interface Props {
		calls: Record<string, number>;
		errors: Record<string, number>;
		/** Rows shown before the "Show all" toggle; the rest are one tap away. */
		limit?: number;
	}
	let { calls, errors, limit = 8 }: Props = $props();

	// An error rate at or above this reads as "worth a look" (danger colour);
	// below it the percentage stays dim so a single flaky call among
	// hundreds doesn't shout.
	const ATTENTION_RATE = 0.1;

	let expanded = $state(false);

	const rows = $derived(
		Object.entries(calls)
			.map(([name, count]) => ({ name, count, errored: errors[name] ?? 0 }))
			.sort((a, b) => b.count - a.count || a.name.localeCompare(b.name))
	);
	const maxCount = $derived(rows[0]?.count ?? 1);
	const visible = $derived(expanded ? rows : rows.slice(0, limit));
</script>

{#if rows.length > 0}
	<div class="tool-bars">
		{#each visible as row (row.name)}
			{@const rate = row.errored / row.count}
			<span class="name" title={row.name}>{row.name}</span>
			<span
				class="bar"
				role="img"
				aria-label="{row.count} calls, {row.errored} errored"
			>
				<i style="width: {(row.count / maxCount) * 100}%"></i>
				{#if row.errored > 0}
					<b
						style="width: {(row.errored / maxCount) * 100}%; left: {((row.count - row.errored) / maxCount) *
							100}%"
					></b>
				{/if}
			</span>
			<span class="n">{row.count}</span>
			<span class="pct" class:bad={rate >= ATTENTION_RATE}>
				{row.errored > 0 ? `${Math.round(rate * 100)}% err` : '—'}
			</span>
		{/each}
	</div>
	{#if rows.length > limit}
		<button class="more" onclick={() => (expanded = !expanded)}>
			{expanded ? 'Show fewer' : `Show all ${rows.length} tools`}
		</button>
	{/if}
{:else}
	<p class="empty">No tool calls in this period.</p>
{/if}

<style>
	.tool-bars {
		display: grid;
		grid-template-columns: minmax(0, 8.5em) 1fr auto 4.5em;
		align-items: center;
		gap: var(--space-sm) var(--space-md);
		padding: var(--space-md) var(--space-lg);
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		font-size: 12px;
	}

	.name {
		font-family: var(--font-mono);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.bar {
		position: relative;
		height: var(--space-sm);
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		overflow: hidden;
	}

	.bar i,
	.bar b {
		position: absolute;
		top: 0;
		bottom: 0;
	}

	.bar i {
		left: 0;
		background: var(--color-accent-2);
	}

	/* Painted over the tail of the call bar, so the red slice reads as "this
	   much of the total failed", not a separate bar stacked after it. */
	.bar b {
		background: var(--color-danger);
	}

	.n,
	.pct {
		font-family: var(--font-mono);
		text-align: right;
	}

	.pct {
		color: var(--color-text-dim);
		font-size: 11px;
	}

	.pct.bad {
		color: var(--color-danger);
	}

	.more {
		margin-top: var(--space-sm);
		padding: var(--space-xs) var(--space-xs);
		background: none;
		border: none;
		color: var(--color-text-dim);
		font-size: 12px;
		cursor: pointer;
	}

	.more:hover {
		color: var(--color-text);
	}

	.empty {
		color: var(--color-text-dim);
		font-size: 13px;
		margin: 0;
	}
</style>
