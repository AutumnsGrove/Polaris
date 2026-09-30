<script lang="ts">
	import { SlidersHorizontal, Globe, X } from '@lucide/svelte';
	import type { SearchResult, RankState } from '$lib/types';
	import { domainOf, faviconHue } from '$lib/searchHelpers';

	// One web result: favicon monogram, title link, snippet, and the per-domain
	// ranking popover. The open/closed state and the persisted-rank logic stay in
	// the page (one popover is open at a time, and rank overrides are keyed by
	// result), so this only renders and reports what the user did.
	let {
		result: r,
		state,
		popoverOpen,
		onTogglePopover,
		onClosePopover,
		onSetRank
	}: {
		result: SearchResult;
		state: RankState;
		popoverOpen: boolean;
		onTogglePopover: () => void;
		onClosePopover: () => void;
		onSetRank: (r: SearchResult, domain: string, state: RankState) => void;
	} = $props();

	const domain = $derived(domainOf(r.url));
	const hue = $derived(faviconHue(domain));

	const rankLabels: Record<RankState, string> = {
		block: 'Block',
		lower: 'Lower',
		default: 'Default',
		raise: 'Raise',
		pin: 'Pin',
		'': 'Default'
	};
</script>

<li class="result">
	<div class="result-top">
		<span class="favicon" style="background: hsl({hue} 45% 45%)">
			{domain.charAt(0).toUpperCase()}
		</span>
		<span class="result-url">{domain}</span>
	</div>
	<div class="result-title-row">
		<h3><a href={r.url} target="_blank" rel="noreferrer">{r.title}</a></h3>
		<div class="result-actions">
			<button
				class="tune-btn"
				class:adjusted={state !== 'default'}
				type="button"
				aria-label={`Adjust ranking for ${domain}`}
				onclick={(e) => {
					e.stopPropagation();
					onTogglePopover();
				}}
			>
				<SlidersHorizontal size={15} />
			</button>
			{#if popoverOpen}
				<div class="rank-popover" role="dialog" aria-label="Domain ranking">
					<div class="popover-head">
						<span class="popover-domain"><Globe size={13} />{domain}</span>
						<button
							class="popover-close"
							type="button"
							aria-label="Close"
							onclick={onClosePopover}
						>
							<X size={14} />
						</button>
					</div>
					<div class="rank-group">
						{#each ['block', 'lower', 'default', 'raise', 'pin'] as opt (opt)}
							<button
								type="button"
								class="rank-option"
								class:selected={state === opt}
								data-state={opt}
								onclick={() => onSetRank(r, domain, opt as RankState)}
							>
								{rankLabels[opt as RankState]}
							</button>
						{/each}
					</div>
					<p class="popover-help">
						{#if state === 'block'}
							This domain will be <b>excluded</b> from results.
						{:else}
							This domain ranks <b>{rankLabels[state].toLowerCase()}</b>.
						{/if}
					</p>
					{#if r.engines && r.engines.length > 0}
						<div class="popover-section">
							<p class="popover-label">Found via</p>
							<div class="engine-chips">
								{#each r.engines as engine (engine)}
									<span class="engine-chip">{engine}</span>
								{/each}
							</div>
						</div>
					{/if}
				</div>
			{/if}
		</div>
	</div>
	<p class="snippet">{r.content}</p>
</li>

<style>
	.result {
		padding: var(--space-lg) var(--space-xs);
		border-bottom: 1px solid var(--line);
	}

	.result:first-of-type {
		padding-top: var(--space-sm);
	}

	.result-top {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-bottom: var(--space-sm);
	}

	.favicon {
		width: 20px;
		height: 20px;
		border-radius: var(--radius-sm);
		flex: none;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 10.5px;
		font-weight: 700;
		color: white;
	}

	.result-url {
		font-size: 12.5px;
		color: var(--ink-muted);
	}

	.result-title-row {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: var(--space-md);
		margin-bottom: var(--space-sm);
		position: relative;
	}

	.result h3 {
		margin: 0;
		font-family: ui-serif, Georgia, serif;
		font-size: 18px;
		font-weight: 600;
		line-height: 1.35;
	}

	.result h3 a {
		color: inherit;
		text-decoration: none;
	}

	.result h3 a:hover {
		color: var(--accent);
		text-decoration: underline;
	}

	.result-actions {
		position: relative;
		flex: none;
	}

	.tune-btn {
		appearance: none;
		border: 1px solid transparent;
		background: transparent;
		border-radius: var(--radius-sm);
		width: 28px;
		height: 28px;
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--ink-faint);
		cursor: pointer;
	}

	.tune-btn:hover {
		background: var(--paper-sunken);
		color: var(--ink-muted);
	}

	.tune-btn.adjusted {
		color: var(--accent);
	}

	.rank-popover {
		position: absolute;
		top: 34px;
		right: 0;
		width: 280px;
		background: var(--paper-raised);
		border: 1px solid var(--line);
		border-radius: var(--radius-md);
		box-shadow:
			0 18px 40px var(--shadow-ambient),
			0 3px 10px var(--shadow-ambient);
		z-index: var(--z-popover);
		padding: var(--space-lg) var(--space-lg) var(--space-lg);
	}

	.popover-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: var(--space-lg);
	}

	.popover-domain {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: 14px;
		font-weight: 600;
		color: var(--ink);
	}

	.popover-domain :global(svg) {
		color: var(--ink-faint);
	}

	.popover-close {
		appearance: none;
		border: none;
		background: transparent;
		color: var(--ink-faint);
		cursor: pointer;
		width: 24px;
		height: 24px;
		border-radius: var(--radius-sm);
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.popover-close:hover {
		background: var(--paper-sunken);
	}

	.rank-group {
		display: grid;
		grid-template-columns: repeat(5, 1fr);
		gap: var(--space-xs);
		margin-bottom: var(--space-md);
	}

	.rank-option {
		appearance: none;
		font-size: 10px;
		font-weight: 600;
		padding: var(--space-sm) var(--space-xs);
		border-radius: var(--radius-sm);
		border: 1px solid var(--line);
		background: var(--paper);
		color: var(--ink-muted);
		cursor: pointer;
		text-align: center;
	}

	.rank-option[data-state='block'].selected {
		background: var(--rank-block-soft);
		color: var(--rank-block);
		border-color: var(--line-strong);
	}

	.rank-option[data-state='lower'].selected {
		background: var(--rank-lower-soft);
		color: var(--ink);
		border-color: var(--line-strong);
	}

	.rank-option[data-state='default'].selected {
		background: var(--rank-default-soft);
		color: var(--ink);
		border-color: var(--line-strong);
	}

	.rank-option[data-state='raise'].selected {
		background: var(--rank-raise-soft);
		color: var(--rank-raise);
		border-color: var(--line-strong);
	}

	.rank-option[data-state='pin'].selected {
		background: var(--rank-pin-soft);
		color: var(--rank-pin);
		border-color: var(--line-strong);
	}

	.popover-help {
		font-size: 12px;
		line-height: 1.5;
		color: var(--ink-faint);
		margin: 0 0 var(--space-md);
	}

	.popover-help b {
		color: var(--ink-muted);
		font-weight: 600;
	}

	.popover-section {
		padding-top: var(--space-md);
		border-top: 1px solid var(--line);
	}

	.popover-label {
		font-size: 10.5px;
		font-weight: 600;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: var(--ink-faint);
		margin: 0 0 var(--space-sm);
	}

	.engine-chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
	}

	.engine-chip {
		font-size: 11.5px;
		font-weight: 600;
		color: var(--ink-muted);
		background: var(--paper-sunken);
		border: 1px solid var(--line);
		border-radius: var(--radius-full);
		padding: var(--space-xs) var(--space-md);
	}

	.snippet {
		margin: 0;
		font-size: 13.5px;
		line-height: 1.55;
		color: var(--ink-muted);
		max-width: 68ch;
	}

	@media (max-width: 640px) {
		.result h3 {
			font-size: 16.5px;
		}
		.rank-popover {
			position: fixed;
			left: 16px;
			right: 16px;
			top: auto;
			bottom: 16px;
			width: auto;
		}
	}
</style>
