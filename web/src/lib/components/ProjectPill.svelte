<script lang="ts">
	import { goto } from '$app/navigation';
	import { FolderOpen } from '@lucide/svelte';
	import { projectColorVar } from '$lib/projects.svelte';
	import type { Project } from '$lib/types';

	// The chat header's "this conversation belongs to a project" marker (docs/
	// plans/projects.md, "Thread header: project indicator"). Without it a
	// thread opened straight from search would give no sign it carries a
	// project's instructions and shared files — context silently in play for
	// the turn but invisible on screen. Tapping it opens the project.
	let { project }: { project: Project } = $props();

	let tint = $derived(projectColorVar(project.color));
</script>

<button
	class="pill"
	style:--tint={tint ?? 'var(--color-text-dim)'}
	onclick={() => goto(`/projects/${project.id}`)}
	title="Project: {project.name}"
	aria-label="Open project {project.name}"
>
	<FolderOpen size={12} />
	<span class="name">{project.name}</span>
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
