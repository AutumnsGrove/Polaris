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
	let { src, loc }: { src: string[]; loc?: string } = $props();

	// <...> form so a URL containing parentheses or spaces can't end the link early.
	let text = $derived(src.map((u) => `[${sourceHostname(u)}](<${u}>)`).join(' '));
</script>

{#if src.length}
	<span class="ui-sources"><UiText {text} {loc} /></span>
{/if}

<style>
	.ui-sources {
		margin-left: var(--space-xs);
	}
</style>
