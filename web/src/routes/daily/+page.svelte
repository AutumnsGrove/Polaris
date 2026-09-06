<script lang="ts">
	import { onMount } from 'svelte';
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
		Microscope,
		Trophy,
		Image as ImageIcon,
		Quote,
		AlertTriangle,
		Newspaper
	} from '@lucide/svelte';
	import type { PulsarDailyBlock } from '$lib/types';
	import PulsarDailyConfigModal from '$lib/components/PulsarDailyConfigModal.svelte';

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
		tech_science: Microscope,
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

	onMount(async () => {
		await Promise.all([pulsarDailyState.loadConfig(), pulsarDailyState.loadEdition('latest')]);
		if (pulsarDailyState.edition) {
			viewedDate = pulsarDailyState.edition.date;
			latestDate = pulsarDailyState.edition.date;
			localStorage.setItem('polaris-daily-last-seen', viewedDate);
			// Clears the sidebar's dot immediately — Sidebar.svelte persists
			// across client-side navigation and only checks
			// hasNewEdition once on its own mount, so opening /daily needs
			// to flip this itself rather than waiting for a future reload.
			pulsarDailyState.hasNewEdition = false;
		}
	});

	function formatDate(dateStr: string): string {
		// Parsed with an explicit local midnight, not new Date(dateStr) —
		// a bare "YYYY-MM-DD" parses as UTC midnight, which displays as the
		// *previous* day in any timezone behind UTC.
		const d = new Date(dateStr + 'T00:00:00');
		return d.toLocaleDateString('en-US', { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' });
	}

	async function goPrevious() {
		if (!viewedDate) return;
		await pulsarDailyState.loadEdition(viewedDate, 'before');
		if (pulsarDailyState.edition) viewedDate = pulsarDailyState.edition.date;
	}

	// goToday reloads the latest edition — also doubles as "forward" nav
	// after going back exactly one step, since a general "next day after
	// this one" endpoint doesn't exist yet (multi-step-back-then-forward
	// browsing is a rare enough path for v1 to leave as a known gap
	// rather than build a whole second query for).
	async function goToday() {
		await pulsarDailyState.loadEdition('latest');
		if (pulsarDailyState.edition) viewedDate = pulsarDailyState.edition.date;
	}

	// expand used to POST to the server and wait for the *entire* turn
	// (every tool call included) to finish before navigating anywhere —
	// there was nothing to watch, and no way to follow along. Now the
	// server only resolves what the seeded message should say; sending it
	// happens over the browser's own live WebSocket connection, exactly
	// the path a typed message already takes, so navigation is immediate
	// and the answer streams in live.
	async function expand(block: PulsarDailyBlock) {
		if (expandingKey) return;
		expandingKey = block.key;
		try {
			const resolved = await pulsarDailyState.resolveExpand(viewedDate, block.key);
			if (!resolved) return;
			// titleSeed: the block's own title + content, not the seeded
			// wrapper message — see gateway/protocol.go's
			// ClientMessage.TitleSeed doc comment for why generating a
			// title straight from the wrapper text broke (a real,
			// observed bug: the title model answered the wrapper's
			// embedded "tell me more" instruction instead of titling it).
			const titleSeed = `${block.title}: ${block.content}`.slice(0, 300);
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
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		<h1 class="page-title">The Daily</h1>
	</div>
	<div class="header-right">
		{#if pulsarDailyState.edition && pulsarDailyState.edition.cost_usd > 0}
			<span class="cost-indicator" title="Total LLM cost to generate this edition">
				<Coins size={14} />
				${pulsarDailyState.edition.cost_usd.toFixed(4)}
			</span>
		{/if}
		<button class="icon-btn" onclick={() => (showConfig = true)} title="Configure The Daily">
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
			<button onclick={goPrevious}>← Previous</button>
			<button class:active={viewedDate === latestDate} onclick={goToday}>Today</button>
		</div>
	</div>

	{#if pulsarDailyState.editionState === 'loading'}
		<p class="empty">Loading today's edition…</p>
	{:else if pulsarDailyState.editionState === 'not-found'}
		<p class="empty">
			No edition yet — Pulsar Daily generates once a day at your configured time. Check back then,
			or configure it now.
		</p>
	{:else if pulsarDailyState.edition}
		<div class="board">
			{#each pulsarDailyState.edition.blocks as block (block.key)}
				{@const Icon = blockIcons[block.key] ?? Newspaper}
				<button
					class="card"
					class:top-story={block.is_top_story}
					disabled={expandingKey === block.key}
					onclick={() => expand(block)}
				>
					{#if block.is_top_story}
						<span class="kicker-label">Top Story</span>
						<h3 class="headline">{block.title}</h3>
						{#if block.image_url}
							<img src={block.image_url} alt="" />
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
							<img src={block.image_url} alt="" />
						{/if}
						<!-- eslint-disable-next-line svelte/no-at-html-tags -->
						<div class="card-body">{@html renderContent(block.content)}</div>
					{/if}
					<div class="expand-hint">
						{expandingKey === block.key ? 'Opening…' : 'Continue in chat →'}
					</div>
				</button>
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

	/* True masonry via CSS multi-column layout, not a fixed-row-span
	   grid — see docs/plans/pulsar-daily.md's "Frontend layout": a
	   fixed-row-span grid clipped real content once column width
	   narrowed. Each .card sizes to its own content and flows into
	   whichever column has room next. */
	.board {
		column-count: 3;
		column-gap: var(--space-lg);
		max-width: 1180px;
		margin: 0 auto;
	}
	@media (max-width: 900px) {
		.board {
			column-count: 2;
		}
	}
	@media (max-width: 620px) {
		.board {
			column-count: 1;
		}
	}

	.card {
		display: inline-block;
		width: 100%;
		break-inside: avoid;
		margin-bottom: var(--space-lg);
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
