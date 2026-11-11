<script lang="ts">
	import { getContext } from 'svelte';
	import DOMPurify from 'dompurify';
	import { marked } from '$lib/markdown';
	import { renderInlineCitations } from '$lib/citations';
	import type { Citation, VerificationMark } from '$lib/types';

	// The inline Markdown subset every `ui` text field accepts: **bold**,
	// `code` and [Title](URL). Run through the same marked -> DOMPurify ->
	// renderInlineCitations pipeline as prose, so a link to one of the turn's
	// tracked sources becomes the same named chip, and an unknown URL stays an
	// ordinary link. Never raw HTML: DOMPurify still runs.
	//
	// Ticks here are NOT the prose chips' "nth occurrence of this URL" rule: a
	// block link is matched by its address (`loc`, below) instead, so no counter
	// has to stay in step with the server across blocks (docs/plans/
	// intelligent-ui.md, "Sourcing and verification").
	//
	// `block` is for the few prose-body fields (a tab's text) where a real model
	// writes a fenced command block: full Markdown instead of the inline subset.
	// Same sanitizer and chip pass, so it adds paragraphs and code, not trust.
	//
	// `loc` is this field's verification address (e.g. "0.2.1.src", see
	// uiblocks/sites.go for the field names): when given, a link whose
	// "<loc>#<n>" has a supported mark gets the "found in source" tick. Omitted
	// means no tick for this field, which is always safe.
	let { text, block = false, loc, chipsInTable = false }: { text: string; block?: boolean; loc?: string; chipsInTable?: boolean } = $props();

	// A getter, not the array, so a citations update mid-stream (sources land
	// while the block is still filling in) re-renders the chips.
	const getCitations = getContext<(() => Citation[]) | undefined>('ui-citations');
	// Marks arrive after the turn ends, so this too is a getter.
	const getMarks = getContext<(() => VerificationMark[] | undefined) | undefined>('ui-verification');

	let html = $derived.by(() => {
		const marks = getMarks?.();
		return renderInlineCitations(
			DOMPurify.sanitize((block ? marked.parse(text) : marked.parseInline(text)) as string),
			getCitations?.() ?? [],
			undefined,
			undefined,
			loc && marks?.length ? { prefix: loc, marks } : undefined,
			chipsInTable
		);
	});
</script>

<!-- A div in block mode: a span cannot validly contain paragraphs or <pre>. -->
{#if block}
	<div class="ui-text">{@html html}</div>
{:else}
	<span class="ui-text">{@html html}</span>
{/if}
