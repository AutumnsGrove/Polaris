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
	let { text }: { text: string } = $props();

	// A getter, not the array, so a citations update mid-stream (sources land
	// while the block is still filling in) re-renders the chips.
	const getCitations = getContext<(() => Citation[]) | undefined>('ui-citations');

	let html = $derived(
		renderInlineCitations(DOMPurify.sanitize(marked.parseInline(text) as string), getCitations?.() ?? [])
	);
</script>

<span class="ui-text">{@html html}</span>
