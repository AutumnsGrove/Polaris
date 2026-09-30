<script lang="ts">
	import { Search as SearchIcon } from '@lucide/svelte';

	// The search box. Rendered in the header once a search has run and centered
	// on the start screen before that; the page owns the query state.
	let { value = $bindable(), onSubmit }: { value: string; onSubmit: (e: Event) => void } = $props();
</script>

<form class="omnibox" onsubmit={onSubmit}>
	<SearchIcon size={16} class="icon-search" />
	<input
		type="text"
		bind:value
		placeholder="Search the web"
		spellcheck="false"
	/>
	<span class="hint">? for answer</span>
</form>

<style>
	.omnibox {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		background: var(--paper-raised);
		border: 1px solid var(--line-strong);
		border-radius: var(--radius-md);
		padding: var(--space-md) var(--space-lg);
	}

	.omnibox:focus-within {
		border-color: var(--accent);
		box-shadow: 0 0 0 3px var(--accent-soft);
	}

	.omnibox :global(.icon-search) {
		flex: none;
		color: var(--ink-faint);
	}

	.omnibox input {
		flex: 1;
		border: none;
		outline: none;
		background: transparent;
		font-size: 16px;
		color: var(--ink);
		min-width: 0;
	}

	.omnibox .hint {
		flex: none;
		font-size: 11px;
		color: var(--ink-faint);
		background: var(--paper-sunken);
		border: 1px solid var(--line);
		border-radius: var(--radius-sm);
		padding: var(--space-xs) var(--space-sm);
		white-space: nowrap;
	}

	@media (max-width: 640px) {
		.omnibox .hint {
			display: none;
		}
	}
</style>
