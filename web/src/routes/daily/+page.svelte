<script lang="ts">
	import { onMount, tick } from 'svelte';
	import { goto } from '$app/navigation';
	import { appState } from '$lib/state.svelte';
	import { pulsarDailyState } from '$lib/pulsarDaily.svelte';
	import { marked } from '$lib/markdown';
	import DOMPurify from 'dompurify';
	import {
		PanelLeft,
		Settings,
		Coins,
		BookOpen,
		CloudSun,
		ScrollText,
		Globe,
		MapPin,
		Flame,
		Trophy,
		Image as ImageIcon,
		Quote,
		AlertTriangle,
		Newspaper
	} from '@lucide/svelte';
	import type { PulsarDailyBlock } from '$lib/types';
	import PulsarDailyConfigModal from '$lib/components/PulsarDailyConfigModal.svelte';
	import ChartCard from '$lib/components/ChartCard.svelte';

	// blockIcons: a Lucide icon component per block key, matching the
	// mockup's visual language — no per-kind structured layout (weather
	// 5-day strip, ranked trending list, ...) like the mockup mocked up,
	// since every block's real content is prose written by an LLM call,
	// not structured data the frontend could lay out specially. Top Story
	// gets its own layout instead of an icon (see the mockup's
	// .top-story treatment: "special means more substance, not a
	// highlight box").
	const blockIcons: Record<string, typeof BookOpen> = {
		word_of_day: BookOpen,
		weather: CloudSun,
		on_this_day: ScrollText,
		headlines: Globe,
		local: MapPin,
		trending: Flame,
		sports: Trophy,
		picture_of_day: ImageIcon,
		quote: Quote,
		notice: AlertTriangle
	};

	// today's viewed date — not necessarily today's actual calendar date,
	// since the ← Previous nav can move this backward.
	let viewedDate = $state('');
	let latestDate = $state('');
	let expandingKey = $state('');
	let showConfig = $state(false);

	// Masonry column assignment — see the .board style comment for why this
	// is JS-driven rather than CSS multi-column. columnCount mirrors the
	// old CSS breakpoints (3 / 2 / 1 at 900px / 620px); boardWidth comes
	// from bind:clientWidth on .board itself.
	let columnCount = $state(3);
	let boardWidth = $state(0);
	let measureEls: Record<string, HTMLElement> = {};
	let heights = $state<Record<string, number>>({});

	function updateColumnCount() {
		if (window.matchMedia('(max-width: 620px)').matches) columnCount = 1;
		else if (window.matchMedia('(max-width: 900px)').matches) columnCount = 2;
		else columnCount = 3;
	}

	const gapPx = $derived(
		typeof document !== 'undefined'
			? parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--space-lg')) || 16
			: 16
	);
	const perColumnWidth = $derived(
		boardWidth > 0 ? (boardWidth - gapPx * (columnCount - 1)) / columnCount : 0
	);

	// Re-measure whenever the edition, column count, or board width
	// changes. Reads heights are NOT taken here (only written), so this
	// can't loop on its own writes.
	$effect(() => {
		const blocks = pulsarDailyState.edition?.blocks ?? [];
		// Referencing these keeps the effect reactive to their changes even
		// though they're not used directly below (measureEls reads the
		// live DOM instead).
		void columnCount;
		void perColumnWidth;
		if (!blocks.length) return;
		tick().then(() => {
			const next: Record<string, number> = {};
			for (const b of blocks) {
				const el = measureEls[b.key];
				if (el) next[b.key] = el.offsetHeight;
			}
			heights = next;
		});
	});

	// Greedy shortest-column assignment — a single very tall card no
	// longer starves other columns the way CSS column-balancing did,
	// because real measured heights (not a naive total/N estimate) decide
	// placement.
	const columns = $derived.by(() => {
		const blocks = pulsarDailyState.edition?.blocks ?? [];
		const cols: PulsarDailyBlock[][] = Array.from({ length: columnCount }, () => []);
		const colHeights = new Array(columnCount).fill(0);
		for (const b of blocks) {
			let target = 0;
			for (let i = 1; i < columnCount; i++) {
				if (colHeights[i] < colHeights[target]) target = i;
			}
			cols[target].push(b);
			colHeights[target] += (heights[b.key] ?? 0) + gapPx;
		}
		return cols;
	});

	onMount(() => {
		// onMount's own return value is only used as a cleanup callback
		// when onMount's callback is synchronous — an async callback's
		// resolved value is ignored, so the data-loading half runs in a
		// fire-and-forget inner async function instead of making this
		// whole callback async.
		updateColumnCount();
		window.addEventListener('resize', updateColumnCount);
		(async () => {
			await Promise.all([pulsarDailyState.loadConfig(), pulsarDailyState.loadEdition('latest')]);
			if (pulsarDailyState.edition) {
				viewedDate = pulsarDailyState.edition.date;
				latestDate = pulsarDailyState.edition.date;
				localStorage.setItem('polaris-daily-last-seen', viewedDate);
				// Clears the sidebar's dot immediately — Sidebar.svelte
				// persists across client-side navigation and only checks
				// hasNewEdition once on its own mount, so opening /daily
				// needs to flip this itself rather than waiting for a
				// future reload.
				pulsarDailyState.hasNewEdition = false;
			}
		})();
		return () => window.removeEventListener('resize', updateColumnCount);
	});

	function formatDate(dateStr: string): string {
		// Parsed with an explicit local midnight, not new Date(dateStr) —
		// a bare "YYYY-MM-DD" parses as UTC midnight, which displays as the
		// *previous* day in any timezone behind UTC.
		const d = new Date(dateStr + 'T00:00:00');
		return d.toLocaleDateString('en-US', { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' });
	}

	// Short form for the sticky header — the full dateline lives in the
	// masthead, which scrolls out of view with the rest of the content
	// (this page's .content, not the document, owns scrolling). Without
	// this, scrolling into the cards left no visible cue for which day
	// you were viewing except scrolling all the way back up — a real
	// complaint, reproduced live by scrolling past the masthead on a
	// non-today edition.
	function formatShortDate(dateStr: string): string {
		const d = new Date(dateStr + 'T00:00:00');
		return d.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric' });
	}

	// atEarliestEdition backs a one-shot "you've reached the earliest
	// edition" notice — a real bug found live via design critique: without
	// this, clicking ← Previous past the oldest edition left the sticky
	// header's date pill (and the masthead dateline) still showing the
	// last valid date while the body dropped into the generic "No edition
	// yet" empty state, implying that date itself had nothing generated
	// rather than "there's nothing earlier than this." Sticky until the
	// next successful nav (Today/Next/a Previous that actually lands
	// somewhere), not auto-dismissed on a timer — there's nothing else to
	// do about it until the user moves on anyway.
	let atEarliestEdition = $state(false);

	async function goPrevious() {
		if (!viewedDate) return;
		const priorEdition = pulsarDailyState.edition;
		const priorDate = viewedDate;
		await pulsarDailyState.loadEdition(viewedDate, 'before');
		if (pulsarDailyState.edition) {
			viewedDate = pulsarDailyState.edition.date;
			atEarliestEdition = false;
		} else {
			// Nothing earlier exists — restore what was already on screen
			// instead of leaving the header and body disagreeing about
			// which date failed to load.
			pulsarDailyState.edition = priorEdition;
			pulsarDailyState.editionState = 'loaded';
			viewedDate = priorDate;
			atEarliestEdition = true;
		}
	}

	// goNext mirrors goPrevious via NextDailyEdition — previously the only
	// way "forward" was possible at all was re-fetching "latest", which
	// broke the moment a user stepped back more than one day (no "day
	// after this one" query existed to walk forward one step at a time).
	async function goNext() {
		if (!viewedDate || viewedDate === latestDate) return;
		await pulsarDailyState.loadEdition(viewedDate, 'after');
		if (pulsarDailyState.edition) viewedDate = pulsarDailyState.edition.date;
		atEarliestEdition = false;
	}

	async function goToday() {
		await pulsarDailyState.loadEdition('latest');
		if (pulsarDailyState.edition) viewedDate = pulsarDailyState.edition.date;
		atEarliestEdition = false;
	}

	// expand used to POST to the server and wait for the *entire* turn
	// (every tool call included) to finish before navigating anywhere —
	// there was nothing to watch, and no way to follow along. Now the
	// server only resolves what the seeded message should say; sending it
	// happens over the browser's own live WebSocket connection, exactly
	// the path a typed message already takes, so navigation is immediate
	// and the answer streams in live. itemIndex, when given, scopes this
	// to one story within a list-shaped block's items instead of the
	// whole block — the per-story "Continue in chat" affordance.
	// expandingKey uses a composite `key:index` identifier in that case so
	// only that one item's button shows "Opening…", not every item in the
	// same card.
	async function expand(block: PulsarDailyBlock, itemIndex?: number) {
		const trackingKey = itemIndex === undefined ? block.key : `${block.key}:${itemIndex}`;
		if (expandingKey) return;
		expandingKey = trackingKey;
		try {
			const resolved = await pulsarDailyState.resolveExpand(viewedDate, block.key, itemIndex);
			if (!resolved) return;
			// titleSeed: the story's own title + summary (or the block's,
			// for a whole-block expand), not the seeded wrapper message —
			// see gateway/protocol.go's ClientMessage.TitleSeed doc comment
			// for why generating a title straight from the wrapper text
			// broke (a real, observed bug: the title model answered the
			// wrapper's embedded "tell me more" instruction instead of
			// titling it).
			const seedSource =
				itemIndex !== undefined && block.items
					? block.items[itemIndex]
					: { title: block.title, summary: block.content };
			const titleSeed = `${seedSource.title}: ${seedSource.summary}`.slice(0, 300);
			appState.newThread();
			await goto('/');
			appState.send(
				resolved.content,
				undefined,
				undefined,
				undefined,
				resolved.attachment_id
					? {
							id: resolved.attachment_id,
							filename: resolved.attachment_filename ?? '',
							content_type: resolved.attachment_content_type ?? '',
							size_bytes: 0
						}
					: undefined,
				undefined,
				'pulsar-daily',
				titleSeed
			);
		} finally {
			expandingKey = '';
		}
	}

	function renderContent(content: string): string {
		return DOMPurify.sanitize(marked.parse(content || '') as string);
	}

	const droppedCount = $derived(
		pulsarDailyState.config && pulsarDailyState.edition
			? Math.max(0, pulsarDailyState.config.enabled_blocks.length - pulsarDailyState.edition.blocks.length)
			: 0
	);
</script>

<svelte:head>
	<title>The Daily — Polaris</title>
</svelte:head>

<header class="header">
	<div class="header-left">
		{#if !appState.sidebarOpen}
			<button
				class="icon-btn"
				onclick={() => appState.toggleSidebar()}
				title="Open sidebar"
				aria-label="Open sidebar"
			>
				<PanelLeft size={18} />
			</button>
		{/if}
		<h1 class="page-title">The Daily</h1>
		{#if viewedDate}
			<span class="header-date" class:not-today={viewedDate !== latestDate}>
				{formatShortDate(viewedDate)}
			</span>
		{/if}
	</div>
	<div class="header-right">
		{#if pulsarDailyState.edition && pulsarDailyState.edition.cost_usd > 0}
			<span class="cost-indicator" title="Total LLM cost to generate this edition">
				<Coins size={14} />
				${pulsarDailyState.edition.cost_usd.toFixed(4)}
			</span>
		{/if}
		<button
			class="icon-btn"
			onclick={() => (showConfig = true)}
			title="Configure The Daily"
			aria-label="Configure The Daily"
		>
			<Settings size={18} />
		</button>
	</div>
</header>

<div class="content">
	<div class="masthead">
		<p class="kicker">Pulsar</p>
		<h2 class="wordmark">THE DAILY</h2>
		{#if viewedDate}
			<div class="dateline"><span>{formatDate(viewedDate)}</span></div>
		{/if}
		<div class="edition-nav">
			<button disabled={atEarliestEdition} onclick={goPrevious}>← Previous</button>
			<button class:active={viewedDate === latestDate} onclick={goToday}>Today</button>
			<button disabled={viewedDate === latestDate} onclick={goNext}>Next →</button>
		</div>
		{#if atEarliestEdition}
			<p class="nav-boundary-note">You've reached the earliest edition.</p>
		{/if}
	</div>

	{#if pulsarDailyState.editionState === 'loading'}
		<p class="empty">Loading today's edition…</p>
	{:else if pulsarDailyState.editionState === 'not-found'}
		<p class="empty">
			No edition yet — Pulsar Daily generates once a day at your configured time. Check back then,
			or configure it now.
		</p>
	{:else if pulsarDailyState.edition}
		{#snippet card(block: PulsarDailyBlock, measuring: boolean)}
			{@const Icon = blockIcons[block.key] ?? Newspaper}
			{#if !block.is_top_story && block.items?.length}
				<!-- List-shaped block (headlines/trending/custom) — each story
				     is its own independently expandable row instead of one
				     prose blob with a single all-or-nothing "Continue in
				     chat", per the plan doc's per-story expansion design. Not
				     a <button> itself (unlike every other card shape here):
				     the card as a whole isn't one clickable affordance, each
				     item row is. -->
				<div class="card items-card" aria-hidden={measuring}>
					<div class="card-head">
						<div class="card-icon">
							<Icon size={15} />
						</div>
						<div class="card-title">{block.title}</div>
					</div>
					{#each block.items as item, i (i)}
						{@const trackingKey = `${block.key}:${i}`}
						<button
							class="item-row"
							disabled={expandingKey === trackingKey}
							tabindex={measuring ? -1 : 0}
							onclick={() => !measuring && expand(block, i)}
						>
							<div class="item-title">{item.title}</div>
							<p class="item-summary">{item.summary}</p>
							{#if item.source}
								<span class="item-source">{item.source}</span>
							{/if}
							<div class="expand-hint">
								{expandingKey === trackingKey ? 'Opening…' : 'Continue in chat →'}
							</div>
						</button>
					{/each}
				</div>
			{:else}
				<button
					class="card"
					class:top-story={block.is_top_story}
					disabled={expandingKey === block.key}
					tabindex={measuring ? -1 : 0}
					aria-hidden={measuring}
					onclick={() => !measuring && expand(block)}
				>
					{#if block.is_top_story}
						<span class="kicker-label">Top Story</span>
						<h3 class="headline">{block.title}</h3>
						{#if block.image_url}
							<img src={block.image_url} alt={block.title} />
						{/if}
						<!-- eslint-disable-next-line svelte/no-at-html-tags -->
						<div class="card-body">{@html renderContent(block.content)}</div>
					{:else}
						<div class="card-head">
							<div class="card-icon">
								<Icon size={15} />
							</div>
							<div class="card-title">{block.title}</div>
						</div>
						{#if block.key === 'picture_of_day' && block.image_url}
							<img src={block.image_url} alt={block.title} />
						{/if}
						<!-- eslint-disable-next-line svelte/no-at-html-tags -->
						<div class="card-body">{@html renderContent(block.content)}</div>
						{#if block.chart}
							<!-- Weather's own structured forecast (setWeatherChart) —
							     the same ChartCard a normal chat turn's weather tool
							     gets, so Daily's card isn't stuck with plain prose
							     just because it's a dailyBlockDirect dispatch. The
							     forecast bullet list this would otherwise duplicate
							     is already stripped server-side (see
							     TrimWeatherForecastSection). -->
							<ChartCard chart={block.chart} />
						{/if}
					{/if}
					<div class="expand-hint">
						{expandingKey === block.key ? 'Opening…' : 'Continue in chat →'}
					</div>
				</button>
			{/if}
		{/snippet}

		<!-- Invisible reference copies at the real per-column width, purely
		     so the $effect above can read each card's true rendered
		     offsetHeight before deciding which visible column it goes in.
		     Not interactive (aria-hidden, tabindex -1, click no-op). -->
		<div class="board-measure" style="width: {perColumnWidth}px" aria-hidden="true">
			{#each pulsarDailyState.edition.blocks as block (block.key)}
				<div bind:this={measureEls[block.key]}>
					{@render card(block, true)}
				</div>
			{/each}
		</div>

		<div class="board" bind:clientWidth={boardWidth}>
			{#each columns as col, i (i)}
				<div class="board-column">
					{#each col as block (block.key)}
						{@render card(block, false)}
					{/each}
				</div>
			{/each}
		</div>

		{#if droppedCount > 0}
			<p class="unchanged-note">
				{droppedCount} block{droppedCount === 1 ? '' : 's'} had nothing new today — silently skipped
			</p>
		{/if}
	{/if}
</div>

{#if showConfig}
	<PulsarDailyConfigModal onClose={() => (showConfig = false)} />
{/if}

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: max(var(--space-lg), env(safe-area-inset-top)) var(--space-lg) var(--space-lg);
		box-shadow: var(--shadow-well);
	}
	.header-left {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		min-width: 0;
	}
	.header-right {
		display: flex;
		align-items: center;
		gap: var(--space-md);
	}
	.cost-indicator {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		font-size: 12.5px;
		color: var(--color-text-dim);
	}
	.page-title {
		margin: 0;
		font-family: var(--font-serif);
		font-size: 20px;
		font-weight: 700;
	}
	/* Stays visible in the sticky header after the masthead's own full
	   dateline scrolls out of view — see formatShortDate's doc comment. */
	.header-date {
		font-size: 12px;
		color: var(--color-text-dim);
		padding: 2px var(--space-sm);
		border-radius: var(--radius-full);
		border: 1px solid transparent;
		white-space: nowrap;
	}
	.header-date.not-today {
		color: var(--color-accent);
		border-color: var(--color-accent);
		background: var(--color-accent-soft);
	}
	.content {
		flex: 1;
		overflow-y: auto;
		padding: 0 var(--space-lg) var(--space-2xl);
	}
	.empty {
		max-width: 46ch;
		margin: var(--space-2xl) auto;
		text-align: center;
		font-size: 13.5px;
		line-height: 1.6;
		color: var(--color-text-dim);
	}

	.masthead {
		text-align: center;
		padding: var(--space-xl) 0;
		border-bottom: 1px solid var(--color-border);
		margin-bottom: var(--space-xl);
	}
	.masthead .kicker {
		color: var(--color-text-dim);
		font-size: 11px;
		letter-spacing: 0.18em;
		text-transform: uppercase;
		margin: 0;
	}
	.masthead .wordmark {
		font-family: var(--font-wordmark);
		font-size: 1.7rem;
		letter-spacing: 0.06em;
		color: var(--color-accent);
		margin: var(--space-xs) 0 0;
	}
	.masthead .dateline {
		margin-top: var(--space-md);
		font-size: 13.5px;
		color: var(--color-text-dim);
	}
	.edition-nav {
		display: flex;
		justify-content: center;
		gap: var(--space-sm);
		margin-top: var(--space-lg);
	}
	.edition-nav button {
		font: inherit;
		font-size: 12.5px;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		color: var(--color-text-dim);
		padding: var(--space-xs) var(--space-md);
		border-radius: var(--radius-full);
		cursor: pointer;
	}
	.edition-nav button.active {
		background: var(--color-accent-soft);
		border-color: var(--color-accent);
		color: var(--color-accent);
	}
	.edition-nav button:disabled {
		cursor: default;
		opacity: 0.4;
	}
	.nav-boundary-note {
		margin: var(--space-sm) 0 0;
		font-size: 12px;
		color: var(--color-text-dim);
	}

	/* True masonry via CSS multi-column layout, not a fixed-row-span
	   grid — see docs/plans/pulsar-daily.md's "Frontend layout": a
	   fixed-row-span grid clipped real content once column width
	   narrowed. Each .card sizes to its own content and flows into
	   whichever column has room next. */
	/* JS-computed masonry (see script's `columns` derivation), not CSS
	   multi-column — column-fill's "balance" mode estimates each column's
	   target height as totalHeight / columnCount, then packs greedily; a
	   single very tall card (the real, Stage-C-elaborated Top Story) throws
	   that estimate off badly enough that a whole column goes unused.
	   column-fill: auto fixes the balance heuristic but needs an explicit
	   container height to fill sequentially against, and there's no way to
	   know that height in advance without measuring anyway — so we measure
	   real card heights in JS and assign greedily to the shortest column
	   ourselves instead of asking the browser to guess. */
	.board {
		display: flex;
		align-items: flex-start;
		gap: var(--space-lg);
		max-width: 1180px;
		margin: 0 auto;
	}
	.board-column {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
		flex: 1 1 0;
		min-width: 0;
	}
	.board-measure {
		position: absolute;
		visibility: hidden;
		pointer-events: none;
		top: 0;
		left: -9999px;
	}

	.card {
		display: block;
		width: 100%;
		background: var(--color-surface);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-sm), var(--shadow-glass-edge);
		padding: var(--space-lg);
		text-align: left;
		font: inherit;
		color: inherit;
		cursor: pointer;
		transition:
			transform 0.15s ease,
			box-shadow 0.15s ease,
			border-color 0.15s ease;
	}
	.card:hover,
	.card:focus-visible {
		transform: translateY(-2px);
		border-color: var(--color-border-strong);
		box-shadow: var(--shadow-md), var(--shadow-glass-edge);
	}
	.card:disabled {
		cursor: default;
		opacity: 0.7;
	}
	.card:hover .expand-hint,
	.card:focus-visible .expand-hint {
		opacity: 1;
	}

	/* items-card is a plain container, not itself clickable (each row
	   below is its own button) — override .card's pointer cursor/hover
	   lift, which only make sense on a whole-card affordance. */
	.items-card {
		cursor: default;
	}
	.items-card:hover,
	.items-card:focus-visible {
		transform: none;
		border-color: var(--color-border);
		box-shadow: var(--shadow-sm), var(--shadow-glass-edge);
	}

	.item-row {
		display: block;
		width: 100%;
		background: none;
		border: none;
		border-top: 1px solid var(--color-border);
		padding: var(--space-md) 0 0;
		margin-top: var(--space-md);
		text-align: left;
		font: inherit;
		color: inherit;
		cursor: pointer;
	}
	.item-row:first-of-type {
		border-top: none;
		padding-top: 0;
		margin-top: 0;
	}
	.item-row:disabled {
		cursor: default;
		opacity: 0.7;
	}
	.item-row:hover .expand-hint,
	.item-row:focus-visible .expand-hint {
		opacity: 1;
	}
	.item-title {
		font-weight: 600;
		font-size: 13.5px;
		color: var(--color-text);
	}
	.item-summary {
		margin: var(--space-xs) 0 0;
		font-size: 13px;
		color: var(--color-text-dim);
	}
	.item-source {
		display: inline-block;
		margin-top: var(--space-xs);
		font-size: 11px;
		color: var(--color-text-dim);
		opacity: 0.7;
	}

	.card-head {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-bottom: var(--space-md);
	}
	.card-icon {
		width: 28px;
		height: 28px;
		display: grid;
		place-items: center;
		border-radius: var(--radius-md);
		background: var(--color-surface-2);
		color: var(--color-accent-2);
		flex-shrink: 0;
	}
	.card-title {
		font-weight: 600;
		font-size: 14.5px;
		letter-spacing: 0.01em;
	}
	.card-body {
		color: var(--color-text-dim);
		font-size: 13.5px;
	}
	.card-body :global(p) {
		margin: 0 0 var(--space-sm);
	}
	.card-body :global(p:last-child) {
		margin-bottom: 0;
	}
	/* LLM-elaborated content (the Top Story especially) sometimes includes
	   markdown headings. Left unstyled they inherit raw UA h1-h6 sizes
	   (up to 2em, bold), which balloons that one card far past its
	   siblings. Scoped down and set in the serif face so a heading still
	   reads as a heading — just via family/weight, not sheer size. */
	.card-body :global(h1),
	.card-body :global(h2),
	.card-body :global(h3),
	.card-body :global(h4) {
		font-family: var(--font-serif);
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text);
		line-height: 1.3;
		margin: var(--space-md) 0 var(--space-xs);
	}
	.card-body :global(h1:first-child),
	.card-body :global(h2:first-child),
	.card-body :global(h3:first-child),
	.card-body :global(h4:first-child) {
		margin-top: 0;
	}
	.card img {
		width: 100%;
		border-radius: var(--radius-md);
		margin-bottom: var(--space-sm);
		display: block;
	}

	/* Top Story — per the plan doc, "special" means more substance, not a
	   highlight border. No accent frame, just more room and a bigger
	   headline. */
	.card.top-story {
		padding: var(--space-xl);
	}
	.top-story .kicker-label {
		font-size: 10.5px;
		letter-spacing: 0.12em;
		text-transform: uppercase;
		color: var(--color-accent);
		margin-bottom: var(--space-xs);
		display: block;
	}
	.top-story .headline {
		font-size: 1.2rem;
		font-weight: 600;
		color: var(--color-text);
		line-height: 1.3;
		margin: 0 0 var(--space-md);
	}
	.top-story .card-body {
		font-size: 14px;
	}

	.expand-hint {
		margin-top: var(--space-md);
		font-size: 11.5px;
		color: var(--color-accent-2);
		opacity: 0.55;
		transition: opacity 0.15s ease;
	}
	.top-story .expand-hint {
		opacity: 1;
		margin-top: var(--space-lg);
		font-size: 13px;
		font-weight: 500;
		color: var(--color-bg);
		background: var(--color-accent);
		width: fit-content;
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-full);
	}

	.unchanged-note {
		margin-top: var(--space-xl);
		text-align: center;
		font-size: 12.5px;
		color: var(--color-text-dim);
	}
</style>
