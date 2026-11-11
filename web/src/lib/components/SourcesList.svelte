<script lang="ts">
	import { ChevronRight, CheckCheck, X } from '@lucide/svelte';
	import type { Citation } from '$lib/types';
	import { sourceHostname as hostname } from '$lib/citations';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';

	let { citations }: { citations: Citation[] } = $props();

	// One compact strip under the answer; the full list lives in a bottom
	// sheet (same shared .modal-backdrop/.modal-panel shell TurnInfoSheet
	// uses). It replaced a collapsed toggle that expanded into a wall of
	// chips *inside* the thread — long answers got pushed around by it, and
	// on a phone the chips sat far from the thumb. The sheet overlays
	// instead of reflowing, and inline citation chips (see
	// renderInlineCitations) still open a source directly, so this isn't the
	// only way in.
	let sheetOpen = $state(false);

	// "Read" is web_read having actually fetched the page (Citation.opened),
	// as opposed to a URL that only showed up in a search snippet — the
	// distinction the old chip cloud couldn't show.
	let readCount = $derived(citations.filter((c) => c.opened).length);
	let checkedCount = $derived(citations.filter((c) => c.verified).length);

	let stripDetail = $derived(
		[readCount ? `${readCount} read` : '', checkedCount ? `${checkedCount} checked` : '']
			.filter(Boolean)
			.join(' · ')
	);

	// Built as a string, not inline markup: Svelte trims whitespace at {#if}
	// block edges, which glued "read in full" to the "·" that follows it.
	let sheetDetail = $derived(
		[
			readCount ? `${readCount} read in full` : '',
			readCount && citations.length > readCount
				? `${citations.length - readCount} from search results`
				: ''
		]
			.filter(Boolean)
			.join(' · ')
	);

	// The strip previews at most four distinct sites, not the first four
	// citations — five reads of go.dev shouldn't render as one tile repeated.
	const STACK_MAX = 4;
	let stackPreview = $derived.by(() => {
		const seen = new Set<string>();
		const out: Citation[] = [];
		for (const c of citations) {
			const host = hostname(c.url);
			if (seen.has(host)) continue;
			seen.add(host);
			out.push(c);
			if (out.length === STACK_MAX) break;
		}
		return out;
	});

	// Citation carries no favicon, so a site gets a monogram tile instead:
	// its first letters, tinted by a hue hashed from the hostname so the same
	// site is the same color in the strip, the sheet, and every other turn.
	function monogram(url: string): string {
		const host = hostname(url).replace(/^www\./, '');
		return host.replace(/[^a-z0-9]/gi, '').slice(0, 2) || '?';
	}

	function hue(url: string): number {
		const host = hostname(url);
		let h = 0;
		for (let i = 0; i < host.length; i++) h = (h * 31 + host.charCodeAt(i)) % 360;
		return h;
	}

	// ~230 wpm, same constant UiReadings uses for its reading-time total. A
	// PDF only ever had one page's text extracted, so it shows pages instead.
	function readLength(c: Citation): string {
		if (c.pages) return `${c.pages} ${c.pages === 1 ? 'page' : 'pages'}`;
		if (c.words) return `${Math.max(1, Math.ceil(c.words / 230))} min read`;
		return '';
	}

	function rowMeta(c: Citation): string {
		return [hostname(c.url), readLength(c)].filter(Boolean).join(' · ');
	}

	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && sheetOpen) sheetOpen = false;
	}
</script>

<svelte:window onkeydown={onKeydown} />

<button class="sources-strip" onclick={() => (sheetOpen = true)} aria-haspopup="dialog">
	<span class="stack" aria-hidden="true">
		{#each stackPreview as c (c.url)}
			{#if c.image_url}
				<img class="tile stack-tile" src={c.image_url} alt="" loading="lazy" />
			{:else}
				<span class="tile stack-tile" style:--hue={hue(c.url)}>{monogram(c.url)}</span>
			{/if}
		{/each}
	</span>
	<span class="strip-label">
		{citations.length}
		{citations.length === 1 ? 'source' : 'sources'}
		{#if stripDetail}<small>{stripDetail}</small>{/if}
	</span>
	<ChevronRight size={14} class="strip-chevron" />
</button>

{#if sheetOpen}
	<div class="modal-backdrop" role="presentation">
		<button class="modal-backdrop-close" onclick={() => (sheetOpen = false)} aria-label="Close"
		></button>
		<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Sources">
			<div
				class="sheet-handle"
				use:swipeToDismiss={() => (sheetOpen = false)}
				aria-hidden="true"
			></div>
			<div class="modal-panel-header">
				<h2>Sources</h2>
				<button class="icon-btn" onclick={() => (sheetOpen = false)} title="Close"
					><X size={18} /></button
				>
			</div>

			{#if sheetDetail}
				<p class="sheet-sub">{sheetDetail}</p>
			{/if}

			<div class="rows">
				{#each citations as c (c.url)}
					<a class="row" href={c.url} target="_blank" rel="noreferrer">
						{#if c.image_url}
							<img class="tile row-tile" src={c.image_url} alt="" loading="lazy" />
						{:else}
							<span class="tile row-tile" style:--hue={hue(c.url)}>{monogram(c.url)}</span>
						{/if}
						<span class="row-text">
							<span class="row-title">{c.title || hostname(c.url)}</span>
							<span class="row-meta">{rowMeta(c)}</span>
						</span>
						{#if c.verified}
							<CheckCheck size={14} class="row-verified" aria-label="Checked against this source" />
						{/if}
					</a>
				{/each}
			</div>

			<!-- Explains what an absent check mark does and doesn't mean — see
			     docs/plans/source-verification-badge.md's UI section: "no mark"
			     covers three different real states (not checked, not supported,
			     below confidence threshold), so it should never read as "this
			     source is bad." Lives in the sheet, not a title tooltip: hover
			     tooltips don't exist on the phone this is primarily used from. -->
			{#if checkedCount}
				<p class="sheet-note">
					<CheckCheck size={12} /> A check mark means a specific claim was checked against that source.
					No mark doesn't mean the source is wrong — just not (yet) checked.
				</p>
			{/if}
		</div>
	</div>
{/if}

<style>
	.sources-strip {
		width: 100%;
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
		color: var(--color-text);
		text-align: left;
		transition:
			background-color 0.15s var(--ease-out-expo),
			border-color 0.15s var(--ease-out-expo);
	}

	.sources-strip:hover {
		background: var(--color-surface-2);
		border-color: var(--color-border-strong);
	}

	.stack {
		display: flex;
	}

	/* Monogram tile — tinted from --hue (hashed per hostname). Dark theme
	   wants light tints on a dark tile; the light theme flips the text lower
	   so the letters keep contrast on a pale tile. */
	.tile {
		--tile-l: 78%;
		flex-shrink: 0;
		display: grid;
		place-items: center;
		background: color-mix(in oklch, oklch(var(--tile-l) 0.1 var(--hue, 235)) 20%, var(--color-surface-2));
		color: oklch(var(--tile-l) 0.1 var(--hue, 235));
		font-size: 10.5px;
		font-weight: 600;
		text-transform: uppercase;
		object-fit: cover;
	}

	:global(:root[data-theme='light']) .tile {
		--tile-l: 44%;
	}

	/* Overlap is a negative gap-sized margin; the ring matches the strip's
	   own background so overlapping tiles read as separate discs. */
	.stack-tile {
		width: 24px;
		height: 24px;
		border-radius: var(--radius-full);
		border: 2px solid var(--color-surface);
		margin-left: calc(var(--space-sm) * -1);
		box-sizing: border-box;
	}

	.stack-tile:first-child {
		margin-left: 0;
	}

	.strip-label {
		flex: 1;
		font-size: 12.5px;
	}

	.strip-label small {
		display: block;
		font-size: 10.5px;
		color: var(--color-text-dim);
	}

	.sources-strip :global(.strip-chevron) {
		color: var(--color-text-dim);
		flex-shrink: 0;
	}

	.sheet-sub {
		margin: calc(var(--space-md) * -1) 0 var(--space-sm);
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.rows {
		display: flex;
		flex-direction: column;
	}

	.row {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: var(--space-sm) 0;
		color: var(--color-text);
		text-decoration: none;
	}

	.row + .row {
		border-top: 1px solid var(--color-border);
	}

	.row:hover .row-title {
		text-decoration: underline;
		text-decoration-color: var(--color-border-strong);
		text-underline-offset: 3px;
	}

	.row-tile {
		width: 32px;
		height: 32px;
		border-radius: var(--radius-md);
		font-size: 12px;
	}

	.row-text {
		min-width: 0;
		flex: 1;
		display: flex;
		flex-direction: column;
	}

	/* Two lines before truncating — the old chips cut a 90-character arXiv
	   title to one line, which is the main reason to have a sheet at all. */
	.row-title {
		font-size: 13px;
		line-height: 1.35;
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	.row-meta {
		font-size: 11px;
		color: var(--color-text-dim);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	/* --color-accent-2: the app's existing "citation chrome / informational"
	   hue, same as the inline chip's own mark. */
	.row :global(.row-verified) {
		flex-shrink: 0;
		color: var(--color-accent-2);
	}

	.sheet-note {
		display: flex;
		gap: var(--space-sm);
		align-items: flex-start;
		margin: var(--space-lg) 0 0;
		font-size: 11.5px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	.sheet-note :global(svg) {
		flex-shrink: 0;
		margin-top: 2px;
		color: var(--color-accent-2);
	}
</style>
