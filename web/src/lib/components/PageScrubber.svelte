<script lang="ts">
	import { ChevronLeft, ChevronRight } from '@lucide/svelte';

	// Atlas's page navigation: the stretched "Atl…as" wordmark (jump to any page)
	// above the precise numbered/chevron row. letterCount is how many a's to
	// offer; see the page's pageLetterCount for why that's optimistic.
	let {
		page,
		hasMore,
		letterCount,
		onGoTo
	}: {
		page: number;
		hasMore: boolean;
		letterCount: number;
		onGoTo: (n: number) => void;
	} = $props();
</script>

<!-- Google's own stretched-logo page picker, Atlas's take —
     see letterCount's doc comment for why this shows the
     full run of 10 up front rather than growing one letter
     per page actually reached. Centered, on its own, above
     the numbered/chevron row below — that one stays the
     precise, always-correct way to move a page at a time;
     this one is the fun, speculative "jump anywhere" one. -->
<div class="page-wordmark" role="group" aria-label="Jump to a page">
	<span
		>Atl{#each Array.from({ length: letterCount }, (_, i) => i + 1) as n (n)}<button
				type="button"
				class="pw-a"
				class:active={n === page}
				disabled={n === page}
				aria-label={`Page ${n}`}
				onclick={() => onGoTo(n)}>a</button
			>{/each}s</span
	>
</div>
<nav class="pagination" aria-label="Search result pages">
	<button
		type="button"
		class="page-nav"
		disabled={page <= 1}
		onclick={() => onGoTo(page - 1)}
		aria-label="Previous page"
	>
		<ChevronLeft size={16} />
	</button>
	{#each Array.from({ length: Math.min(page, 12) }, (_, i) => i + 1) as n (n)}
		<button
			type="button"
			class="page-num"
			class:active={n === page}
			aria-current={n === page ? 'page' : undefined}
			onclick={() => onGoTo(n)}
		>
			{n}
		</button>
	{/each}
	<button
		type="button"
		class="page-nav"
		disabled={!hasMore}
		onclick={() => onGoTo(page + 1)}
		aria-label="Next page"
	>
		<ChevronRight size={16} />
	</button>
</nav>

<style>
	/* The stretched-wordmark page picker — see pageLetterCount's doc
	   comment. Its own row, centered, above the precise numbered/chevron
	   pagination below rather than folded into either the header or that
	   row. */
	.page-wordmark {
		display: flex;
		justify-content: center;
		margin: var(--space-xs) 0 var(--space-lg);
	}

	.page-wordmark span {
		font-family: var(--font-wordmark);
		font-size: 26px;
		font-weight: 400;
		letter-spacing: 0.02em;
		color: var(--ink-muted);
	}

	/* Each "a" is a real page link. Real padding on all sides, not just
	   letter-spacing on the parent — this needs to be an actually
	   tappable target on a phone, not just a visually-spaced glyph, so
	   the hit area is padding (which is part of the target) rather than
	   margin (which isn't). disabled (the current page's own letter)
	   gets the "you are here" color without a separate affordance for
	   "this one doesn't do anything". */
	.pw-a {
		appearance: none;
		border: none;
		background: transparent;
		padding: var(--space-sm) var(--space-xs);
		margin: 0;
		font: inherit;
		color: inherit;
		cursor: pointer;
		border-radius: var(--radius-sm);
	}

	.pw-a:hover:not(:disabled) {
		color: var(--accent);
		background: var(--paper-sunken);
	}

	.pw-a.active {
		color: var(--accent);
		cursor: default;
	}

	.pw-a:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}

	.pagination {
		display: flex;
		align-items: center;
		justify-content: center;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin-top: var(--space-md);
		padding-top: var(--space-xl);
	}

	.page-nav,
	.page-num {
		appearance: none;
		border: 1px solid var(--line);
		background: var(--paper-raised);
		color: var(--ink-muted);
		border-radius: var(--radius-sm);
		cursor: pointer;
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.page-nav {
		width: 34px;
		height: 34px;
		flex: none;
	}

	.page-nav:disabled {
		opacity: 0.4;
		cursor: default;
	}

	.page-nav:not(:disabled):hover {
		border-color: var(--line-strong);
		color: var(--ink);
	}

	.page-num {
		min-width: 34px;
		height: 34px;
		padding: 0 var(--space-xs);
		font-size: 13px;
		font-weight: 600;
	}

	.page-num:hover {
		border-color: var(--line-strong);
		color: var(--ink);
	}

	.page-num.active {
		background: var(--accent-soft);
		border-color: var(--accent-soft-line);
		color: var(--accent);
	}
</style>
