<script lang="ts">
	import { Brain, Cpu, Orbit, SearchSlash, SlidersHorizontal } from '@lucide/svelte';
	import { FOCUS_MODES } from '$lib/focusModes';
	import { appState } from '$lib/state.svelte';
	import type { Project } from '$lib/types';

	// Icon-led chips for a project's NON-default settings only — a project
	// left entirely at defaults shows nothing, keeping the common case quiet
	// (docs/plans/projects.md, "Icon language"). The same glyphs head the
	// matching rows in the detail view's settings, so seeing one on a card
	// trains the read used inside it. Presence of a chip is the signal; the
	// title says exactly what it means, since an icon alone can't.
	let { project }: { project: Project } = $props();

	// A specific focus mode drops the generic sliders glyph for that mode's
	// own icon and shows no label — each mode already has a distinct icon in
	// FOCUS_MODES, so naming it again in words is redundant once recognized.
	let focus = $derived(
		project.default_focus_mode && project.default_focus_mode !== 'off'
			? FOCUS_MODES.find((m) => m.id === project.default_focus_mode)
			: undefined
	);
	let modelName = $derived(appState.models.find((m) => m.id === project.default_model)?.name ?? project.default_model);
	let any = $derived(
		!!focus ||
			project.default_focus_mode === 'off' ||
			!!project.default_model ||
			project.memory_mode === 'none' ||
			!project.constellation_visible ||
			project.exclude_from_chat_search
	);
</script>

{#if any}
	<div class="chips">
		{#if focus}
			{@const Icon = focus.icon}
			<span class="chip" title="{focus.label} focus"><Icon size={12} /></span>
		{:else if project.default_focus_mode === 'off'}
			<span class="chip" title="Starts with no focus mode"><SlidersHorizontal size={12} /></span>
		{/if}
		{#if project.default_model}
			<span class="chip" title="Model: {modelName}"><Cpu size={12} /></span>
		{/if}
		{#if project.memory_mode === 'none'}
			<span class="chip" title="Memory off in this project"><Brain size={12} /></span>
		{/if}
		{#if !project.constellation_visible}
			<span class="chip" title="Hidden from Constellation"><Orbit size={12} /></span>
		{/if}
		{#if project.exclude_from_chat_search}
			<span class="chip" title="Excluded from chat search"><SearchSlash size={12} /></span>
		{/if}
	</div>
{/if}

<style>
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-xs);
	}

	.chip {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		color: var(--color-text-dim);
	}
</style>
