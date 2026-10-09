<script lang="ts">
	import { mountMermaidStream, type MermaidStream } from '$lib/mermaid';

	// One ```mermaid fence of an answer. Owns a stable host node that the
	// streaming renderer (mermaid.ts) mutates directly: the diagram can't live
	// inside the answer's {@html}, which is re-set whenever the text changes
	// and would wipe a render mid-stream. ChatTurnView keys this by segment
	// index, so it survives every token that only grows the fence.
	let { src, closed }: { src: string; closed: boolean } = $props();

	let host = $state<HTMLElement>();
	let stream: MermaidStream | undefined;

	$effect(() => {
		if (!host) return;
		stream ??= mountMermaidStream(host);
		stream.update(src, closed);
	});

	$effect(() => () => stream?.destroy());
</script>

<div class="mermaid-block" bind:this={host}></div>
