<script lang="ts">
	import { getContext } from 'svelte';
	import { sourceHostname } from '$lib/citations';
	import type { Citation } from '$lib/types';
	import type { ReadingItem, UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';

	// Pages to read, in order. Two rules make this send the reader to the
	// sources rather than around them:
	//
	//  1. Only pages Polaris actually opened are shown. A line whose URL has no
	//     `opened` citation (a search snippet, a URL the model half-remembered)
	//     is dropped and counted in the muted note — the model cannot
	//     recommend reading it never did.
	//  2. The minutes and the page count are measured server-side from the raw
	//     page (tools/page_stats.go), never written by the model and never
	//     derived from the filtered summary it saw.
	//
	// Ticks are local state only, like the checklist block: the page remembers
	// nothing beyond the open tab.
	let { block }: { block: Extract<UiBlock, { kind: 'readings' }> } = $props();

	const getCitations = getContext<(() => Citation[]) | undefined>('ui-citations');

	const LEVELS: Record<ReadingItem['lvl'], string> = {
		start: 'Start here',
		next: 'Next',
		deep: 'Go deeper'
	};
	const LEVEL_RANK: Record<ReadingItem['lvl'], number> = { start: 0, next: 1, deep: 2 };

	// Matched exactly as web_read was called, with a trailing slash tolerated.
	function normUrl(u: string): string {
		return u.replace(/\/+$/, '');
	}

	// A reading can run past an hour (a whole book-length guide, comfortably):
	// "3 hr 33 min", not "213 min". Under an hour stays plain minutes.
	function fmtMinutes(n: number): string {
		if (n < 60) return `${n} min`;
		const h = Math.floor(n / 60);
		const m = n % 60;
		return m ? `${h} hr ${m} min` : `${h} hr`;
	}

	interface Entry {
		item: ReadingItem;
		cit: Citation;
		url: string;
	}

	let read = $state<Record<string, boolean>>({});

	const entries = $derived.by(() => {
		const opened = new Map<string, Citation>();
		for (const c of getCitations?.() ?? []) {
			if (c.opened) opened.set(normUrl(c.url), c);
		}
		const found: Entry[] = [];
		for (const item of block.items) {
			const url = item.src.map(normUrl).find((u) => opened.has(u));
			if (url) found.push({ item, cit: opened.get(url)!, url });
		}
		// Fixed level order (start, next, deep), stable within a level however
		// the model emitted the lines.
		found.sort((a, b) => LEVEL_RANK[a.item.lvl] - LEVEL_RANK[b.item.lvl]);
		return found;
	});

	const dropped = $derived(block.items.length - entries.length);
	const readCount = $derived(entries.filter((e) => read[e.url]).length);
	const minutes = $derived(
		entries.reduce((n, e) => n + (e.cit.words ? Math.max(1, Math.ceil(e.cit.words / 230)) : 0), 0)
	);
	const pdfPages = $derived(entries.reduce((n, e) => n + (e.cit.pages ?? 0), 0));

	function toggle(url: string) {
		read = { ...read, [url]: !read[url] };
	}
</script>

{#if entries.length}
	<div class="ui-readings">
		<div class="rh">
			<span class="eyebrow">Read these next</span>
			{#if block.title}
				<div class="t">{block.title}</div>
			{/if}
			<div class="meta">
				{entries.length} {entries.length === 1 ? 'page' : 'pages'} Polaris opened{#if minutes} · about {fmtMinutes(minutes)}{/if}{#if pdfPages} · plus a {pdfPages}-page PDF{/if}
			</div>
			<div class="prog">
				<span class="bar"><i style="width: {Math.round((100 * readCount) / entries.length)}%"></i></span>
				<span class="count">{readCount} of {entries.length} read</span>
			</div>
		</div>

		{#each entries as e (e.url)}
			<div class="it">
				<input type="checkbox" aria-label="Mark as read" checked={!!read[e.url]} onchange={() => toggle(e.url)} />
				<div>
					<span class="lvl {e.item.lvl}">{LEVELS[e.item.lvl]}</span>
					<a class="ti" href={e.url} target="_blank" rel="noopener noreferrer">{e.item.t}</a>
					{#if e.item.why}
						<p class="why"><UiText text={e.item.why} /></p>
					{/if}
					<div class="facts">
						<span class="opened">● Opened by Polaris</span>
						{#if e.cit.pages}
							<span class="mins">{e.cit.pages} pages</span>
						{:else if e.cit.words}
							<span class="mins">≈ {fmtMinutes(Math.max(1, Math.ceil(e.cit.words / 230)))}</span>
						{/if}
						<span class="host">{sourceHostname(e.url)}</span>
					</div>
				</div>
			</div>
		{/each}

		{#if dropped > 0}
			<div class="left-out"><i aria-hidden="true">⊘</i>{dropped} suggested {dropped === 1 ? 'link was' : 'links were'} left out: Polaris never opened {dropped === 1 ? 'it' : 'them'}.</div>
		{/if}
	</div>
{/if}

<style>
	.ui-readings {
		margin: var(--space-md) 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
		overflow: hidden;
	}

	.rh {
		padding: var(--space-md) var(--space-lg) var(--space-sm);
		background: var(--color-surface-2);
	}

	.eyebrow {
		font-size: 11px;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-dim);
		font-weight: 600;
	}

	.rh .t {
		margin-top: 2px;
		font-family: var(--font-serif);
		font-size: 18px;
		line-height: 1.3;
		overflow-wrap: break-word;
	}

	.meta {
		margin-top: 2px;
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.prog {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-top: var(--space-sm);
		font-family: var(--font-mono);
		font-size: 11.5px;
		color: var(--color-text-dim);
	}

	.bar {
		flex: 1;
		height: 3px;
		border-radius: 2px;
		background: var(--color-border);
		overflow: hidden;
	}

	.bar i {
		display: block;
		height: 100%;
		background: var(--color-accent);
		transition: width 0.25s ease;
	}

	.it {
		display: grid;
		grid-template-columns: 24px 1fr;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.it input {
		appearance: none;
		width: 22px;
		height: 22px;
		margin: 1px 0 0;
		border: 1.5px solid var(--color-border-strong);
		border-radius: 50%;
		display: grid;
		place-items: center;
		cursor: pointer;
	}

	.it input:checked {
		background: var(--color-accent);
		border-color: var(--color-accent);
	}

	.it input:checked::after {
		content: '✓';
		color: var(--color-bg);
		font-size: 13px;
		font-weight: 700;
	}

	.it input:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.lvl {
		font-size: 10.5px;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		font-weight: 700;
		color: var(--color-accent);
	}

	.lvl.deep {
		color: var(--color-accent-2);
	}

	.ti {
		display: block;
		font-weight: 600;
		font-size: 14.5px;
		line-height: 1.35;
		color: var(--color-text);
		text-decoration: underline;
		text-decoration-color: var(--color-border-strong);
		text-underline-offset: 3px;
	}

	.ti:hover {
		text-decoration-color: var(--color-accent);
	}

	.why {
		margin: 2px 0 var(--space-xs);
		font-size: 13px;
		color: var(--color-text-dim);
		overflow-wrap: break-word;
	}

	.facts {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
		font-size: 11.5px;
		color: var(--color-text-dim);
	}

	.opened {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		padding: 1px 9px;
		border-radius: var(--radius-full);
		background: color-mix(in srgb, var(--color-syntax-addition) 14%, transparent);
		color: var(--color-syntax-addition);
		font-weight: 600;
	}

	.mins {
		font-family: var(--font-mono);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-full);
		padding: 0 var(--space-sm);
	}

	.it:has(input:checked) .ti,
	.it:has(input:checked) .why {
		color: var(--color-text-dim);
	}

	.left-out {
		padding: var(--space-sm) var(--space-lg);
		border-top: 1px dashed var(--color-border-strong);
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.left-out i {
		font-style: normal;
		margin-right: 6px;
		color: var(--color-accent);
	}
</style>
