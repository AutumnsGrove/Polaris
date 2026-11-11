<script lang="ts">
	import { untrack } from 'svelte';
	import { marked } from '$lib/markdown';
	import DOMPurify from 'dompurify';
	import { ChevronRight, Loader2, TriangleAlert, Users } from '@lucide/svelte';
	import type { SubAgentItem } from '$lib/types';
	import ToolEvent from './ToolEvent.svelte';

	// One Deep Research researcher (a spawn_researchers task) as a single
	// self-contained card: its reasoning, tool calls and commentary live inside
	// it instead of the main timeline, and its final findings report sits at the
	// bottom once it finishes. Collapsed by default — with several researchers
	// running at once, the point is a calm overview you can open one at a time,
	// not eight live streams competing for the screen.
	let { agent }: { agent: SubAgentItem } = $props();

	// Collapsed unless the researcher failed: a running card shows its live
	// activity line instead of streaming its steps, and a failed one opens so
	// the reason is visible without a click.
	let open = $state(untrack(() => agent.status === 'failed'));

	// "c.2" -> "Researcher 3": the numeric suffix of the agent ID is its
	// position in the spawn call's task list.
	const ordinal = $derived(Number(agent.agentId.split('.').pop()) + 1);

	const toolCalls = $derived(agent.items.filter((i) => i.kind === 'tool').length);

	// What the researcher is doing right now, for the collapsed header of a
	// running card: its newest unfinished tool call, else live reasoning.
	const activity = $derived.by(() => {
		if (agent.status !== 'running') return '';
		for (let i = agent.items.length - 1; i >= 0; i--) {
			const item = agent.items[i];
			if (item.kind === 'tool' && !item.done) {
				if (item.tool === 'web_search') return `Searching: ${item.args?.query ?? ''}`;
				if (item.tool === 'web_read') return `Reading: ${item.args?.url ?? ''}`;
				return item.tool.replace(/_/g, ' ');
			}
			if (item.kind === 'reasoning' && !item.done) return 'Reasoning…';
		}
		return toolCalls ? 'Working…' : 'Starting…';
	});

	const summary = $derived(
		agent.status === 'failed'
			? 'Failed'
			: agent.status === 'done'
				? `${toolCalls} tool call${toolCalls === 1 ? '' : 's'}`
				: ''
	);

	// Findings come back as a markdown-ish bullet list with source URLs.
	const reportHtml = $derived(
		agent.result ? DOMPurify.sanitize(marked.parse(agent.result) as string) : ''
	);
</script>

<div class="subagent" class:failed={agent.status === 'failed'}>
	<button class="subagent-header" onclick={() => (open = !open)} aria-expanded={open}>
		{#if agent.status === 'failed'}
			<TriangleAlert size={13} color="var(--color-accent)" />
		{:else}
			<Users size={13} color="var(--color-accent-2)" />
		{/if}
		<span class="subagent-title">
			<span class="subagent-ordinal">Researcher {ordinal}</span>
			<span class="subagent-objective">{agent.objective}</span>
		</span>
		{#if summary}<span class="subagent-summary">{summary}</span>{/if}
		{#if agent.status === 'running'}
			<Loader2 size={13} color="var(--color-text-dim)" class="spin" />
		{:else}
			<ChevronRight size={13} color="var(--color-text-dim)" class={open ? 'chevron open' : 'chevron'} />
		{/if}
	</button>

	{#if agent.status === 'running' && !open}
		<div class="subagent-activity">{activity}</div>
	{/if}

	{#if open}
		<div class="subagent-body">
			{#if agent.items.length}
				<div class="subagent-steps">
					{#each agent.items as item, i (i)}
						{#if item.kind !== 'subagent'}
							<ToolEvent {item} />
						{/if}
					{/each}
				</div>
			{:else if agent.status === 'running'}
				<div class="subagent-empty">Waiting to start…</div>
			{/if}

			{#if agent.status !== 'running' && reportHtml}
				<div class="subagent-report-label">{agent.status === 'failed' ? 'Error' : 'Findings'}</div>
				<div class="subagent-report">{@html reportHtml}</div>
			{/if}
		</div>
	{/if}
</div>

<style>
	.subagent {
		border: none;
		background: color-mix(in srgb, var(--color-surface-2) 55%, transparent);
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-xs);
		margin-bottom: var(--space-xs);
		font-size: 12px;
		overflow: hidden;
	}

	/* Same accent-tinted glow the compaction chip uses to read as different
	   from an ordinary step, here meaning "this one didn't complete". */
	.subagent.failed {
		box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 30%, transparent), var(--shadow-xs);
	}

	.subagent-header {
		display: flex;
		width: 100%;
		align-items: center;
		gap: var(--space-sm);
		border: none;
		background: transparent;
		padding: var(--space-sm) var(--space-md);
		text-align: left;
		color: var(--color-text-dim);
		transition: background-color 0.15s var(--ease-out-expo), color 0.15s var(--ease-out-expo);
	}

	.subagent-header:hover {
		background: var(--color-surface-2);
		color: var(--color-text);
	}

	.subagent-title {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.subagent-ordinal {
		font-weight: 600;
		color: var(--color-text);
		margin-right: var(--space-sm);
	}

	.subagent-summary {
		font-size: 11.5px;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}

	/* A single quiet line under the header while collapsed and running, so a
	   wave of researchers shows progress without each one needing to be opened. */
	.subagent-activity {
		padding: 0 var(--space-md) var(--space-sm) calc(var(--space-md) + var(--space-lg));
		font-size: 11.5px;
		color: var(--color-text-dim);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.subagent-body {
		padding: var(--space-xs) var(--space-md) var(--space-md) var(--space-md);
	}

	/* The researcher's own steps, set in from the card edge with a hairline
	   rule so they read as belonging to this card, not the main timeline. */
	.subagent-steps {
		padding-left: var(--space-md);
		box-shadow: inset 1px 0 0 color-mix(in srgb, var(--color-accent-2) 35%, transparent);
	}

	.subagent-empty {
		color: var(--color-text-dim);
		padding: var(--space-xs) 0;
	}

	.subagent-report-label {
		margin-top: var(--space-md);
		font-size: 10.5px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-accent-2);
	}

	.subagent-report {
		margin-top: var(--space-xs);
		font-size: 13px;
		line-height: 1.55;
		color: var(--color-text);
		overflow-wrap: anywhere;
	}

	.subagent-report :global(ul) {
		margin: 0;
		padding-left: var(--space-lg);
	}

	.subagent-report :global(p) {
		margin: 0 0 var(--space-xs) 0;
	}

	.subagent-report :global(a) {
		color: var(--color-accent);
	}
</style>
