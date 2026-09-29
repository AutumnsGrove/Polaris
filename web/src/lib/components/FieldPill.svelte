<script lang="ts">
	import { goto } from '$app/navigation';
	import { FolderOpen } from '@lucide/svelte';
	import { fieldColorVar } from '$lib/fields.svelte';
	import type { Field } from '$lib/types';

	// The chat header's "this conversation belongs to a field" marker (docs/
	// plans/fields.md, "Thread header: field indicator"). Without it a
	// thread opened straight from search would give no sign it carries a
	// field's instructions and shared files — context silently in play for
	// the turn but invisible on screen. Tapping it opens the field.
	let { field }: { field: Field } = $props();

	let tint = $derived(fieldColorVar(field.color));
</script>

<button
	class="pill"
	style:--tint={tint ?? 'var(--color-text-dim)'}
	onclick={() => goto(`/fields/${field.id}`)}
	title="Field: {field.name}"
	aria-label="Open Field {field.name}"
>
	<FolderOpen size={12} />
	<span class="name">{field.name}</span>
</button>

<style>
	.pill {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		max-width: 160px;
		flex-shrink: 1;
		min-width: 0;
		padding: 2px var(--space-sm);
		border: 1px solid color-mix(in oklch, var(--tint) 45%, transparent);
		border-radius: var(--radius-full);
		background: color-mix(in oklch, var(--tint) 12%, transparent);
		color: var(--tint);
		font: inherit;
		font-size: 11.5px;
		font-weight: 500;
		cursor: pointer;
		transition: background-color 0.15s var(--ease-out-expo);
	}

	.pill:hover {
		background: color-mix(in oklch, var(--tint) 22%, transparent);
	}

	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
