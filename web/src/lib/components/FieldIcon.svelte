<script lang="ts">
	import { FolderOpen } from '@lucide/svelte';

	// The one place Fields' icon is chosen — the sidebar nav row and pinned
	// header, the hub cards, the chat-header pill, and the composer picker
	// all render this instead of importing a glyph themselves, so swapping
	// the icon (see mockups/field-icons.html) is a one-file change.
	let { size = 16, class: className = '' }: { size?: number; class?: string } = $props();
</script>

<FolderOpen {size} class={className} />
