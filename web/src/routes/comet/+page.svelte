<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { appState } from '$lib/state.svelte';
	import { cometState } from '$lib/comet.svelte';
	import { colorForCategory } from '$lib/categoryColors';
	import {
		PanelLeft,
		Settings,
		Coins,
		MessageSquare,
		Star,
		Search,
		TrendingUp,
		TrendingDown,
		ChevronLeft,
		ChevronRight,
		Ghost,
		Sparkles
	} from '@lucide/svelte';
	import CometConfigModal from '$lib/components/CometConfigModal.svelte';
	import CometIcon from '$lib/components/icons/Comet.svelte';

	let showConfig = $state(false);
	let generating = $state(false);
	let generateError = $state('');

	onMount(() => {
		void cometState.loadConfig();
		void cometState.loadPeriods();
	});

	// The viewed period lives in the URL as ?period=YYYY-MM, so an edition is
	// shareable/bookmarkable and the browser's back/forward buttons work. No
	// period in the URL means "whatever the newest edition is": the first
	// effect loads that, and the second rewrites the URL to the concrete
	// period once it resolves.
	const urlPeriod = $derived(page.url.searchParams.get('period'));

	$effect(() => {
		void cometState.loadEdition(urlPeriod ?? 'latest');
	});

	$effect(() => {
		const edition = cometState.edition;
		// Only canonicalize once a load has actually settled — during a
		// navigation editionState is 'loading' while `edition` still holds the
		// previous period, and rewriting the URL then would fight the click
		// that started it.
		if (!edition || cometState.editionState !== 'loaded') return;
		if (urlPeriod !== edition.period) {
			void goto(`/comet?period=${edition.period}`, { replaceState: true });
		}
		// Opening a period clears the footer icon's unread dot — same "viewed
		// it, it's no longer new" contract Pulsar Daily's own dot has.
		cometState.markSeen(edition.period);
	});

	// periods is newest-first; index 0 is the newest edition. viewedPeriod
	// prefers the loaded edition but falls back to the URL so the arrows
	// resolve correctly even before the first edition lands.
	const viewedPeriod = $derived(cometState.edition?.period ?? urlPeriod ?? '');
	const viewedIndex = $derived(cometState.periods.indexOf(viewedPeriod));
	const atNewest = $derived(viewedIndex <= 0);
	const atOldest = $derived(viewedIndex === -1 || viewedIndex >= cometState.periods.length - 1);

	function goToPeriod(period: string) {
		if (!period) return;
		// A real SvelteKit navigation (not pushState) so page.url updates
		// reactively and the browser's back/forward buttons work — pushState
		// changed the address bar but never re-fired the load effect.
		void goto(`/comet?period=${period}`);
	}
	function goOlder() {
		if (atOldest) return;
		goToPeriod(cometState.periods[viewedIndex + 1]);
	}
	function goNewer() {
		if (atNewest) return;
		goToPeriod(cometState.periods[viewedIndex - 1]);
	}

	// Defensive copies: the JSON on the wire can legitimately carry `null`
	// for an empty collection (Go marshals a nil slice as null), and a bare
	// `data.spiked.length` on one throws and freezes the whole page — the
	// exact bug that made the ← / → arrows appear dead ("Cannot read
	// properties of null (reading 'length')"). The backend now normalizes
	// these too, but the page stays robust to any older edition already
	// stored with nulls.
	const data = $derived(cometState.edition?.data ?? null);
	const categories = $derived(data?.categories ?? []);
	const spiked = $derived(data?.spiked ?? []);
	const faded = $derived(data?.faded ?? []);
	const stars = $derived(data?.stars ?? []);
	const highlights = $derived(data?.highlights ?? []);
	const firsts = $derived(data?.firsts ?? []);
	const tools = $derived(data?.tools ?? []);
	const rhythm = $derived(data?.rhythm ?? null);
	const growth = $derived(data?.growth ?? { new_stars: 0, grown_stars: 0, new_links: 0 });
	// Constellation can be off while Comet is on: every growth number is then
	// zero, and the whole card should disappear rather than show empty.
	const hasConstellation = $derived(
		(data?.star_total ?? 0) > 0 || (growth.grown_stars ?? 0) > 0 || (growth.new_links ?? 0) > 0
	);

	const rhythmMax = $derived(Math.max(...(rhythm?.days ?? []).map((d) => d.count), 1));

	// The heatmap is a 7-column grid aligned to weekday; pad the start with
	// blanks so the first day lands under its real weekday.
	const heatmapOffset = $derived.by(() => {
		const first = rhythm?.days?.[0]?.date;
		if (!first) return 0;
		const [y, m, d] = first.split('-').map(Number);
		return new Date(y, m - 1, d).getDay();
	});
	const heatmapCells = $derived([
		...Array.from({ length: heatmapOffset }, () => null),
		...(rhythm?.days ?? [])
	]);

	function hourLabel(h: number): string {
		if (h === 0) return '12am';
		if (h === 12) return '12pm';
		return h < 12 ? `${h}am` : `${h - 12}pm`;
	}
	function shortDate(d: string): string {
		const [y, m, day] = d.split('-').map(Number);
		if (!y) return d;
		return new Date(y, m - 1, day).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
	}
	function toolLabel(name: string): string {
		return name.replaceAll('_', ' ');
	}

	// A manual run can take minutes — poll for the new edition to appear
	// rather than blocking. Stops as soon as the target period shows up (or
	// after ~10 minutes, so a wedged run doesn't poll forever).
	async function generateNow() {
		generating = true;
		generateError = '';
		const target = cometState.currentPeriod || 'latest';
		const result = await cometState.generateNow();
		if (result.error) {
			generateError = result.error;
			generating = false;
			return;
		}
		appState.showToast('Comet is generating — this can take a few minutes');
		const before = cometState.periods.length;
		for (let i = 0; i < 120; i++) {
			await new Promise((r) => setTimeout(r, 5000));
			await cometState.loadPeriods();
			const appeared = cometState.periods.length > before || cometState.periods.includes(target);
			// A re-generation of an existing period doesn't change the period
			// list length, so also stop once the config's last_generated_at
			// moves past where it was when we started.
			await cometState.loadConfig();
			if (appeared || cometState.config?.last_generated_at) {
				await cometState.loadEdition('latest');
				break;
			}
		}
		generating = false;
	}

	const statIcons = { turns: MessageSquare, stars: Star, searches: Search, cost: Coins };

	function periodLabel(period: string): string {
		// period is "YYYY-MM"; render "September 2026" without pulling in a
		// date library — new Date(y, m-1, 1) is local-time and unambiguous here.
		const [y, m] = period.split('-').map(Number);
		if (!y || !m) return period;
		return new Date(y, m - 1, 1).toLocaleDateString(undefined, { month: 'long', year: 'numeric' });
	}
	function shortPeriod(period: string): string {
		const [y, m] = period.split('-').map(Number);
		if (!y || !m) return period;
		return new Date(y, m - 1, 1).toLocaleDateString(undefined, { month: 'short', year: 'numeric' });
	}
</script>

<svelte:head>
	<title>Comet — Polaris</title>
</svelte:head>

<header class="header">
	<div class="header-left">
		{#if !appState.sidebarOpen}
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar" aria-label="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		<h1 class="page-title">Comet</h1>
		{#if cometState.edition}
			<span class="header-period">{shortPeriod(cometState.edition.period)}</span>
		{/if}
	</div>
	<div class="header-right">
		{#if cometState.edition && cometState.edition.cost_usd > 0}
			<span class="cost-indicator" title="LLM cost to assemble this edition">
				<Coins size={14} />
				${cometState.edition.cost_usd.toFixed(4)}
			</span>
		{/if}
		<button class="icon-btn" onclick={() => (showConfig = true)} title="Configure Comet" aria-label="Configure Comet">
			<Settings size={18} />
		</button>
	</div>
</header>

<div class="content">
	<div class="masthead">
		<p class="kicker">Monthly retrospective</p>
		<h2 class="wordmark">Comet</h2>
		{#if cometState.edition}
			<p class="dateline">{periodLabel(cometState.edition.period)}</p>
			{#if cometState.edition.hook}
				<p class="hook">{cometState.edition.hook}</p>
			{/if}
			{#if highlights.length > 0}
				<ul class="highlights">
					{#each highlights as h (h.thread_id)}
						<li>
							<a href={`/t/${h.thread_id}`}>{h.title}</a>
							{#if h.why}<span class="why"> — {h.why}</span>{/if}
						</li>
					{/each}
				</ul>
			{/if}
		{/if}
		<div class="edition-nav">
			<button disabled={atOldest || cometState.periods.length === 0} onclick={goOlder} aria-label="Previous month">
				<ChevronLeft size={16} />
			</button>
			<span class="current">{cometState.edition ? periodLabel(cometState.edition.period) : '—'}</span>
			<button disabled={atNewest || cometState.periods.length === 0} onclick={goNewer} aria-label="Next month">
				<ChevronRight size={16} />
			</button>
		</div>
	</div>

	{#if cometState.editionState === 'loading'}
		<p class="empty">Loading…</p>
	{:else if cometState.editionState === 'error'}
		<p class="empty">
			Couldn't reach the server — check your connection and
			<button class="retry-link" onclick={() => cometState.loadEdition('latest')}>try again</button>.
		</p>
	{:else if cometState.editionState === 'not-found'}
		<div class="empty-block">
			<CometIcon size={32} />
			<p class="empty-title">No retrospective yet</p>
			<p class="empty">
				Comet generates once a month, at the start of each month, covering the one before it. Turn it on
				and generate the most recent month now, or wait for the scheduled run.
			</p>
			<div class="empty-actions">
				<button class="btn btn-accent" onclick={generateNow} disabled={generating}>
					{generating ? 'Generating…' : 'Generate now'}
				</button>
				<button class="btn" onclick={() => (showConfig = true)}>Settings</button>
			</div>
			{#if generateError}<p class="error-text">{generateError}</p>{/if}
		</div>
	{:else if cometState.edition}
		<!-- Block 1 — stat row -->
		<div class="stat-grid">
			<div class="stat-tile">
				<div class="icon"><MessageSquare size={15} /></div>
				<div class="value">{data?.stats.turns ?? 0}</div>
				<div class="label">Turns asked</div>
			</div>
			<div class="stat-tile">
				<div class="icon"><Star size={15} /></div>
				<div class="value">{data?.stats.new_stars ?? 0}</div>
				<div class="label">New stars formed</div>
			</div>
			<div class="stat-tile">
				<div class="icon"><Search size={15} /></div>
				<div class="value">{data?.stats.searches ?? 0}</div>
				<div class="label">Searches run</div>
			</div>
			<div class="stat-tile">
				<div class="icon"><Coins size={15} /></div>
				<div class="value">${(data?.stats.cost_usd ?? 0).toFixed(2)}</div>
				<div class="label">Cost this month</div>
			</div>
		</div>
		{#if (data?.ghost_count ?? 0) > 0}
			<p class="ghost-note" title="How many times ghost mode was used — nothing about the content">
				<Ghost size={13} /> {data?.ghost_count} incognito session{data?.ghost_count === 1 ? '' : 's'}
			</p>
		{/if}

		<!-- Block 2 — category breakdown -->
		{#if categories.length > 0}
			<div class="card">
				<div class="card-head">
					<h2>Where your attention went</h2>
					<span class="meta">by conversation</span>
				</div>
				{#each categories as cat (cat.name)}
					{@const max = Math.max(...categories.map((c) => c.count), 1)}
					<div class="cat-row">
						<span class="dot" style="background:{colorForCategory(cat.name)}"></span>
						<span class="name">{cat.name}</span>
						<div class="bar-track">
							<div class="bar-fill" style="width:{Math.round((cat.count / max) * 100)}%;background:{colorForCategory(cat.name)}"></div>
						</div>
						<span class="count">{cat.count}</span>
					</div>
				{/each}
			</div>
		{/if}

		<!-- New ground — topics explored for the first time -->
		{#if firsts.length > 0}
			<div class="card">
				<div class="card-head">
					<h2>New ground</h2>
					<span class="meta">first time this month</span>
				</div>
				{#each firsts as f (f.name)}
					<div class="first-row">
						<Sparkles size={13} />
						<span class="first-name">{f.name}</span>
						<span class="first-count">{f.count} conversation{f.count === 1 ? '' : 's'}</span>
					</div>
				{/each}
			</div>
		{/if}

		<!-- Block 3 — spiked / faded -->
		{#if spiked.length > 0 || faded.length > 0}
			<div class="split-row">
				<div class="card trend-card spiked">
					<div class="card-head">
						<TrendingUp size={16} />
						<span class="title">What spiked</span>
					</div>
					{#if spiked.length === 0}
						<p class="trend-empty">Nothing stood out this month.</p>
					{:else}
						<ul>
							{#each spiked as item (item.topic)}
								<li><span>{item.topic}</span><span class="delta">{item.delta}</span></li>
							{/each}
						</ul>
					{/if}
				</div>
				<div class="card trend-card faded">
					<div class="card-head">
						<TrendingDown size={16} />
						<span class="title">Quietly faded</span>
					</div>
					{#if faded.length === 0}
						<p class="trend-empty">Nothing went quiet this month.</p>
					{:else}
						<ul>
							{#each faded as item (item.topic)}
								<li><span>{item.topic}</span><span class="delta">{item.since}</span></li>
							{/each}
						</ul>
					{/if}
				</div>
			</div>
		{/if}

		<!-- Rhythm + tools -->
		{#if rhythm}
			<div class="split-row" class:single={tools.length === 0}>
				<div class="card rhythm-card">
					<div class="card-head">
						<h2>Your rhythm</h2>
						<span class="meta">{rhythm.active_days}/{rhythm.days_in_period} days</span>
					</div>
					<div class="heat-head">
						{#each ['S', 'M', 'T', 'W', 'T', 'F', 'S'] as w, i (i)}<span>{w}</span>{/each}
					</div>
					<div class="heat-grid">
						{#each heatmapCells as cell}
							{#if cell}
								{@const level = cell.count === 0 ? 0 : Math.min(4, Math.ceil((cell.count / rhythmMax) * 4))}
								<span class={`heat heat-${level}`} title={`${shortDate(cell.date)}: ${cell.count}`}></span>
							{:else}
								<span class="heat heat-blank"></span>
							{/if}
						{/each}
					</div>
					<p class="rhythm-note">
						Most often around {hourLabel(rhythm.peak_hour)}{#if rhythm.busiest_count > 0}, busiest on {shortDate(rhythm.busiest_date)}{/if}.
					</p>
				</div>
				{#if tools.length > 0}
					<div class="card tools-card">
						<div class="card-head"><h2>Tools you leaned on</h2></div>
						{#each tools.slice(0, 6) as tool (tool.name)}
							<div class="tool-row">
								<span class="tool-name">{toolLabel(tool.name)}</span>
								<div class="tool-track"><div class="tool-fill" style="width:{Math.round((tool.count / tools[0].count) * 100)}%"></div></div>
								<span class="tool-count">{tool.count}</span>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{/if}

		<!-- Constellation growth -->
		{#if hasConstellation}
			<div class="card">
				<div class="card-head">
					<h2>Constellation grew</h2>
					<span class="meta">{data?.star_total ?? 0} new</span>
				</div>
				<p class="growth-line">
					{data?.star_total ?? 0} new star{(data?.star_total ?? 0) === 1 ? '' : 's'}{#if growth.grown_stars > 0} · {growth.grown_stars} grew{/if}{#if growth.new_links > 0} · {growth.new_links} new link{growth.new_links === 1 ? '' : 's'}{/if}
				</p>
				{#if growth.featured}
					<a class="featured-star" href="/constellation" title="Most-revisited star this month">
						<Star size={13} />
						<span class="f-title">{growth.featured.title}</span>
						<span class="f-cat">{growth.featured.category}</span>
					</a>
				{/if}
				{#if stars.length > 0}
					<div class="star-grid">
						{#each stars.slice(0, 4) as star (star.id)}
							<a class="mini-star" href="/constellation" title="Open in Constellation">
								<div class="tile" style="background:color-mix(in srgb, {colorForCategory(star.category)} 18%, transparent); color:{colorForCategory(star.category)}">
									<Star size={13} />
								</div>
								<div>
									<div class="star-title">{star.title}</div>
									<div class="star-cat">{star.category}</div>
								</div>
							</a>
						{/each}
					</div>
				{/if}
				{#if (data?.star_total ?? 0) > 4}
					<a class="more-link" href="/constellation">+{(data?.star_total ?? 0) - 4} more · view in Constellation →</a>
				{/if}
			</div>
		{/if}
	{/if}
</div>

{#if showConfig}
	<CometConfigModal onClose={() => (showConfig = false)} />
{/if}

<style>
	/* .header and .content are flex siblings inside the layout's .main
	   (display:flex; column; overflow:hidden) — the header stays put and
	   .content is the one scroll container, exactly like The Daily and
	   Constellation. Without flex:1/overflow-y on .content, the page was
	   clipped and couldn't scroll at all. */
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border);
		background: var(--color-surface);
	}
	.header-left,
	.header-right {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}
	.page-title {
		font-size: 1rem;
		font-weight: 600;
		margin: 0;
	}
	.header-period {
		color: var(--color-text-dim);
		font-size: 0.8rem;
	}
	.cost-indicator {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		color: var(--color-text-dim);
		font-size: 0.78rem;
	}
	.icon-btn {
		display: grid;
		place-items: center;
		background: transparent;
		border: none;
		color: var(--color-text-dim);
		cursor: pointer;
		padding: var(--space-xs);
		border-radius: var(--radius-md);
	}
	.icon-btn:hover {
		color: var(--color-text);
		background: var(--color-surface-2);
	}

	.content {
		flex: 1;
		overflow-y: auto;
		width: 100%;
		max-width: 760px;
		margin: 0 auto;
		padding: var(--space-2xl) var(--space-xl) var(--space-3xl);
	}

	.masthead {
		position: relative;
		padding: var(--space-xl) var(--space-lg) var(--space-lg);
		background:
			radial-gradient(1px 1px at 20% 30%, rgba(255, 255, 255, 0.5), transparent),
			radial-gradient(1px 1px at 65% 15%, rgba(255, 255, 255, 0.35), transparent),
			radial-gradient(1.5px 1.5px at 85% 55%, rgba(255, 255, 255, 0.4), transparent),
			radial-gradient(1px 1px at 45% 70%, rgba(255, 255, 255, 0.3), transparent),
			linear-gradient(180deg, var(--color-surface), var(--color-bg));
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		text-align: center;
		margin-bottom: var(--space-xl);
	}
	.wordmark {
		font-family: 'Asimovian', var(--font-display, sans-serif);
		font-size: 1.9rem;
		letter-spacing: 0.08em;
		color: var(--color-accent);
		margin: 0;
	}
	.kicker {
		color: var(--color-text-dim);
		font-size: 0.72rem;
		letter-spacing: 0.18em;
		text-transform: uppercase;
		margin: 0 0 var(--space-xs);
	}
	.dateline {
		margin-top: var(--space-sm);
		font-size: 0.9rem;
		color: var(--color-text-dim);
	}
	.hook {
		max-width: 46ch;
		margin: var(--space-sm) auto 0;
		font-size: 0.95rem;
		line-height: 1.5;
	}
	.edition-nav {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-sm);
		margin-top: var(--space-lg);
	}
	.edition-nav button {
		display: grid;
		place-items: center;
		font: inherit;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		color: var(--color-text-dim);
		width: 32px;
		height: 32px;
		border-radius: var(--radius-full);
		cursor: pointer;
	}
	.edition-nav button:disabled {
		opacity: 0.35;
		cursor: default;
	}
	.edition-nav .current {
		font-size: 0.85rem;
		font-weight: 500;
		padding: 0 var(--space-md);
		min-width: 11ch;
		text-align: center;
	}

	.stat-grid {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: var(--space-md);
		margin-bottom: var(--space-md);
	}
	.stat-tile {
		background: var(--color-surface);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		padding: var(--space-lg) var(--space-md);
		text-align: center;
	}
	.stat-tile .icon {
		width: 30px;
		height: 30px;
		margin: 0 auto var(--space-sm);
		display: grid;
		place-items: center;
		border-radius: var(--radius-md);
		background: var(--color-accent-soft);
		color: var(--color-accent);
	}
	.stat-tile .value {
		font-size: 1.4rem;
		font-weight: 600;
		line-height: 1.1;
	}
	.stat-tile .label {
		font-size: 0.7rem;
		color: var(--color-text-dim);
		margin-top: 3px;
	}
	.ghost-note {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-xs);
		color: var(--color-text-dim);
		font-size: 0.72rem;
		margin: 0 0 var(--space-xl);
	}

	.card {
		background: var(--color-surface);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		padding: var(--space-xl);
		margin-bottom: var(--space-xl);
	}
	.card-head {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-sm);
		margin-bottom: var(--space-lg);
	}
	.card-head h2 {
		font-size: 0.95rem;
		font-weight: 600;
		margin: 0;
	}
	.card-head .meta {
		font-size: 0.75rem;
		color: var(--color-text-dim);
	}

	.cat-row {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: var(--space-sm) 0;
	}
	.cat-row .dot {
		width: 9px;
		height: 9px;
		border-radius: var(--radius-full);
		flex-shrink: 0;
	}
	.cat-row .name {
		font-size: 0.85rem;
		width: 15ch;
		flex-shrink: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.cat-row .bar-track {
		flex: 1;
		height: 7px;
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		overflow: hidden;
	}
	.cat-row .bar-fill {
		height: 100%;
		border-radius: var(--radius-full);
	}
	.cat-row .count {
		font-size: 0.78rem;
		color: var(--color-text-dim);
		width: 3ch;
		text-align: right;
		flex-shrink: 0;
	}

	.split-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: var(--space-lg);
		margin-bottom: var(--space-xl);
	}
	.split-row .card {
		margin-bottom: 0;
	}
	.trend-card .card-head {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-bottom: var(--space-md);
	}
	.trend-card.spiked .card-head {
		color: var(--color-accent);
	}
	.trend-card.faded .card-head {
		color: var(--color-text-dim);
	}
	.trend-card .card-head .title {
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text);
	}
	.trend-card ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.trend-card li {
		padding: var(--space-sm) 0;
		border-bottom: 1px solid var(--color-border);
		font-size: 0.85rem;
		display: flex;
		justify-content: space-between;
		gap: var(--space-sm);
	}
	.trend-card li:last-child {
		border-bottom: none;
	}
	.trend-card.faded li {
		color: var(--color-text-dim);
	}
	.trend-card .delta {
		font-size: 0.72rem;
		color: var(--color-text-dim);
		flex-shrink: 0;
	}
	.trend-card.spiked .delta {
		color: var(--color-accent);
	}
	.trend-empty {
		color: var(--color-text-dim);
		font-size: 0.82rem;
		margin: 0;
	}

	.star-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: var(--space-sm);
	}
	.mini-star {
		display: flex;
		gap: var(--space-sm);
		padding: var(--space-md);
		border-radius: var(--radius-md);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		color: inherit;
		text-decoration: none;
	}
	.mini-star:hover {
		border-color: var(--color-border-strong);
	}
	.mini-star .tile {
		width: 30px;
		height: 30px;
		border-radius: var(--radius-sm);
		flex-shrink: 0;
		display: grid;
		place-items: center;
	}
	.mini-star .star-title {
		font-size: 0.82rem;
		font-weight: 500;
		line-height: 1.3;
	}
	.mini-star .star-cat {
		font-size: 0.68rem;
		color: var(--color-text-dim);
		margin-top: 2px;
	}
	.more-link {
		display: block;
		text-align: center;
		margin-top: var(--space-lg);
		font-size: 0.82rem;
		color: var(--color-accent-2);
		text-decoration: none;
	}

	.empty-block {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-3xl) var(--space-lg);
		color: var(--color-text-dim);
		text-align: center;
	}
	.empty-title {
		color: var(--color-text);
		font-weight: 600;
		margin: 0;
	}
	.empty {
		color: var(--color-text-dim);
		font-size: 0.9rem;
		line-height: 1.5;
		max-width: 46ch;
		margin: 0 auto;
	}
	.empty-actions {
		display: flex;
		gap: var(--space-sm);
		margin-top: var(--space-sm);
	}
	.retry-link {
		background: none;
		border: none;
		color: var(--color-accent);
		cursor: pointer;
		font: inherit;
		padding: 0;
	}
	.error-text {
		color: var(--color-danger, oklch(65% 0.19 25));
		font-size: 0.82rem;
		margin: var(--space-sm) 0 0;
	}

	.highlights {
		list-style: none;
		margin: var(--space-md) 0 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}
	.highlights li {
		font-size: 0.82rem;
		line-height: 1.4;
	}
	.highlights a {
		color: var(--color-accent-2);
		text-decoration: none;
	}
	.highlights a:hover {
		text-decoration: underline;
	}
	.highlights .why {
		color: var(--color-text-dim);
	}

	.split-row.single {
		grid-template-columns: 1fr;
	}

	.first-row {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) 0;
		border-bottom: 1px solid var(--color-border);
		color: var(--color-accent);
	}
	.first-row:last-child {
		border-bottom: none;
	}
	.first-name {
		flex: 1;
		font-size: 0.85rem;
		color: var(--color-text);
	}
	.first-count {
		font-size: 0.72rem;
		color: var(--color-text-dim);
	}

	.heat-head,
	.heat-grid {
		display: grid;
		grid-template-columns: repeat(7, 1fr);
		gap: 3px;
		max-width: 260px;
		margin: 0 auto;
	}
	.heat-head {
		margin-bottom: var(--space-xs);
	}
	.heat-head span {
		font-size: 0.62rem;
		color: var(--color-text-dim);
		text-align: center;
	}
	.heat {
		aspect-ratio: 1;
		border-radius: 3px;
		background: var(--color-surface-3);
	}
	.heat-blank {
		background: transparent;
	}
	.heat-1 {
		background: color-mix(in srgb, var(--color-accent) 22%, var(--color-surface-3));
	}
	.heat-2 {
		background: color-mix(in srgb, var(--color-accent) 45%, var(--color-surface-3));
	}
	.heat-3 {
		background: color-mix(in srgb, var(--color-accent) 68%, var(--color-surface-3));
	}
	.heat-4 {
		background: var(--color-accent);
	}
	.rhythm-note {
		font-size: 0.72rem;
		color: var(--color-text-dim);
		margin: var(--space-md) 0 0;
		text-align: center;
	}

	.tool-row {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: 5px 0;
	}
	.tool-name {
		font-size: 0.8rem;
		width: 12ch;
		flex-shrink: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.tool-track {
		flex: 1;
		height: 6px;
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		overflow: hidden;
	}
	.tool-fill {
		height: 100%;
		border-radius: var(--radius-full);
		background: var(--color-accent-2);
	}
	.tool-count {
		font-size: 0.75rem;
		color: var(--color-text-dim);
		width: 5ch;
		text-align: right;
	}

	.growth-line {
		font-size: 0.85rem;
		margin: 0 0 var(--space-md);
	}
	.featured-star {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		margin-bottom: var(--space-md);
		border-radius: var(--radius-md);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		color: var(--color-text-dim);
		text-decoration: none;
		font-size: 0.82rem;
	}
	.featured-star .f-title {
		color: var(--color-text);
		flex: 1;
	}
	.featured-star .f-cat {
		font-size: 0.7rem;
	}

	@media (max-width: 620px) {
		.stat-grid {
			grid-template-columns: repeat(2, 1fr);
		}
		.split-row {
			grid-template-columns: 1fr;
		}
		.star-grid {
			grid-template-columns: 1fr;
		}
		.cat-row .name {
			width: 11ch;
			font-size: 0.78rem;
		}
	}
</style>
