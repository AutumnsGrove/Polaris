<script lang="ts">
	import { Brain, Cpu, Galaxy, SearchSlash, SlidersHorizontal } from '@lucide/svelte';
	import { FOCUS_MODES } from '$lib/focusModes';
	import { appState } from '$lib/state.svelte';
	import type { Field } from '$lib/types';

	// Icon-led chips for a field's NON-default settings only — a field
	// left entirely at defaults shows nothing, keeping the common case quiet
	// (docs/plans/fields.md, "Icon language"). The same glyphs head the
	// matching rows in the detail view's settings, so seeing one on a card
	// trains the read used inside it. Presence of a chip is the signal; the
	// title says exactly what it means, since an icon alone can't.
	let { field }: { field: Field } = $props();

	// A specific focus mode drops the generic sliders glyph for that mode's
	// own icon and shows no label — each mode already has a distinct icon in
	// FOCUS_MODES, so naming it again in words is redundant once recognized.
	let focus = $derived(
		field.default_focus_mode && field.default_focus_mode !== 'off'
			? FOCUS_MODES.find((m) => m.id === field.default_focus_mode)
			: undefined
	);
	let modelName = $derived(appState.models.find((m) => m.id === field.default_model)?.name ?? field.default_model);
	let any = $derived(
		!!focus ||
			field.default_focus_mode === 'off' ||
			!!field.default_model ||
			field.memory_mode !== 'default' ||
			!field.constellation_visible ||
			field.exclude_from_chat_search
	);
</script>

{#if any}
	<div class="chips">
		{#if focus}
			{@const Icon = focus.icon}
			<span class="chip" title="{focus.label} focus"><Icon size={12} /></span>
		{:else if field.default_focus_mode === 'off'}
			<span class="chip" title="Starts with no focus mode"><SlidersHorizontal size={12} /></span>
		{/if}
		{#if field.default_model}
			<span class="chip" title="Model: {modelName}"><Cpu size={12} /></span>
		{/if}
		{#if field.memory_mode === 'none'}
			<span class="chip" title="Memory off in this Field"><Brain size={12} /></span>
		{:else if field.memory_mode === 'field_only' || field.memory_mode === 'both'}
			<span class="chip" title={field.memory_mode === 'both' ? 'Field memories + your regular ones' : 'Field-only memories'}><Brain size={12} /></span>
		{/if}
		{#if !field.constellation_visible}
			<span class="chip struck" title="Hidden from Constellation">
				<Galaxy size={12} />
				<svg class="strike" viewBox="0 0 24 24" width="12" height="12" aria-hidden="true">
					<path class="strike-gap" d="M3 21 21 3" />
					<path class="strike-line" d="M3 21 21 3" />
				</svg>
			</span>
		{/if}
		{#if field.exclude_from_chat_search}
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

	/* A chip's presence normally means "this setting is on", so a bare Galaxy
	   read as Constellation being enabled when the field is actually hidden
	   from it. The strike marks it as off; the gap stroke (chip-background
	   colored, wider than the line) keeps the line legible over the glyph at
	   12px. */
	.struck {
		position: relative;
	}

	.strike {
		position: absolute;
		inset: 0;
		margin: auto;
		fill: none;
		stroke-linecap: round;
	}

	.strike-gap {
		stroke: var(--color-surface-3);
		stroke-width: 6;
	}

	.strike-line {
		stroke: currentColor;
		stroke-width: 2;
	}
</style>
