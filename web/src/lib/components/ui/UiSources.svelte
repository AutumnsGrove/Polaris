<script lang="ts">
	import { sourceHostname } from '$lib/citations';
	import UiText from './UiText.svelte';

	// Chips for a row's or block's `"src":[...]` — sources with no natural
	// place in the text. Routed through UiText as a markdown link per URL so a
	// tracked citation gets the identical named chip prose gets, and an
	// unknown URL falls back to a plain domain link (the plan's rule).
	//
	// `loc` is the src field's verification address ("0.2.1.src"); the nth
	// tracked URL in the array is link #n, which is how the server numbers it.
	// `chipsInTable` is for a src field rendered inside a compare table's <td>:
	// these links are source markers, not the cell's data, so they may chip
	// there (see renderInlineCitations).
	let { src, loc, chipsInTable = false }: { src: string[]; loc?: string; chipsInTable?: boolean } = $props();

	// <...> form so a URL containing parentheses or spaces can't end the link early.
	let text = $derived(src.map((u) => `[${sourceHostname(u)}](<${u}>)`).join(' '));
</script>

{#if src.length}
	<span class="ui-sources"><UiText {text} {loc} {chipsInTable} /></span>
{/if}

<style>
	.ui-sources {
		margin-left: var(--space-xs);
	}
</style>
