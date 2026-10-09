<script lang="ts">
	import { getContext } from 'svelte';
	import DOMPurify from 'dompurify';
	import { marked } from '$lib/markdown';
	import { renderInlineCitations } from '$lib/citations';
	import type { Citation } from '$lib/types';

	// The inline Markdown subset every `ui` text field accepts: **bold**,
	// `code` and [Title](URL). Run through the same marked -> DOMPurify ->
	// renderInlineCitations pipeline as prose, so a link to one of the turn's
	// tracked sources becomes the same named chip, and an unknown URL stays an
	// ordinary link. Never raw HTML: DOMPurify still runs.
	//
	// No verification marks are passed on purpose. The server's claim
	// extraction and the client's occurrence counter both skip `ui` links in
	// P1 (docs/plans/intelligent-ui.md, "Verification marks"), and they must
	// agree: turning on either side alone would put ticks on the wrong chips.
	//
	// `block` is for the few prose-body fields (a tab's text) where a real model
	// writes a fenced command block: full Markdown instead of the inline subset.
	// Same sanitizer and chip pass, so it adds paragraphs and code, not trust.
	let { text, block = false }: { text: string; block?: boolean } = $props();

	// A getter, not the array, so a citations update mid-stream (sources land
	// while the block is still filling in) re-renders the chips.
	const getCitations = getContext<(() => Citation[]) | undefined>('ui-citations');

	let html = $derived(
		renderInlineCitations(
			DOMPurify.sanitize((block ? marked.parse(text) : marked.parseInline(text)) as string),
			getCitations?.() ?? []
		)
	);
</script>

<!-- A div in block mode: a span cannot validly contain paragraphs or <pre>. -->
{#if block}
	<div class="ui-text">{@html html}</div>
{:else}
	<span class="ui-text">{@html html}</span>
{/if}
