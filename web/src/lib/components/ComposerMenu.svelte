<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import type { FocusMode } from '$lib/types';
	import {
		Plus,
		Image as ImageIcon,
		Cpu,
		Zap,
		GraduationCap,
		Newspaper,
		Lightbulb,
		MessageCircleQuestion,
		Microscope,
		Check,
		X
	} from '@lucide/svelte';

	// Everything that used to be separate controls (model picker, focus
	// modes, deep research, attach) is consolidated into one "+"-triggered
	// popup — mobile is the primary surface here (see PRODUCT.md), and a
	// row of five-plus small buttons across the composer doesn't survive
	// phone width. Same centered-modal pattern as SettingsPanel.svelte
	// (not a distinct bottom-sheet component) so the app has exactly one
	// popup treatment, not two competing ones.
	let {
		focusMode = $bindable<FocusMode>('off'),
		deepResearch = $bindable(false),
		onAttach
	}: {
		focusMode: FocusMode;
		deepResearch: boolean;
		onAttach: (file: File) => void;
	} = $props();

	let open = $state(false);
	let fileInput: HTMLInputElement | undefined = $state();

	const FOCUS_MODES: { id: FocusMode; label: string; description: string; icon: typeof Zap }[] = [
		{ id: 'brief', label: 'Brief', description: 'Same research, shorter replies', icon: Zap },
		{ id: 'academic', label: 'Academic', description: 'Prefer academic sources', icon: GraduationCap },
		{ id: 'news', label: 'News', description: 'Prefer news sources', icon: Newspaper },
		{
			id: 'first_principles',
			label: 'First Principles',
			description: 'Reason up from fundamentals',
			icon: Lightbulb
		},
		{
			id: 'socratic',
			label: 'Socratic',
			description: 'Explore through guided questions',
			icon: MessageCircleQuestion
		}
	];

	function close() {
		open = false;
	}

	function selectFocus(id: FocusMode) {
		// Tapping the already-active mode turns it back off — a toggle,
		// same shape as AudioPlayer's readAloud-the-active-turn pattern.
		focusMode = focusMode === id ? 'off' : id;
		close();
	}

	function selectModel(id: string) {
		appState.selectedModel = id;
		close();
	}

	function toggleDeepResearch() {
		deepResearch = !deepResearch;
		close();
	}

	function handleFileChange(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		if (file) onAttach(file);
		input.value = '';
		close();
	}

	// A bare "+" icon gave zero indication of anything non-default being
	// active — closing the popup meant losing sight of which focus mode/
	// deep research was on without reopening it. The trigger stays a
	// generic "More" (the model itself isn't the kind of state that needs
	// surfacing at a glance the way an active focus mode does) but grows
	// small badges for whatever's actually turned on.
	let activeFocusLabel = $derived(FOCUS_MODES.find((m) => m.id === focusMode)?.label ?? null);
</script>

<button type="button" class="trigger" onclick={() => (open = true)} aria-label="Attach, focus modes, and model">
	<Plus size={16} />
	<span class="trigger-label">More</span>
	{#if activeFocusLabel}
		<span class="trigger-badge">{activeFocusLabel}</span>
	{/if}
	{#if deepResearch}
		<span class="trigger-badge deep">Deep research</span>
	{/if}
</button>

{#if open}
	<div class="backdrop" role="presentation">
		<button class="backdrop-close" onclick={close} aria-label="Close"></button>
		<div class="panel" role="dialog" aria-modal="true" aria-label="Composer options">
			<div class="panel-header">
				<h2>More</h2>
				<button class="icon-btn" onclick={close} title="Close"><X size={18} /></button>
			</div>

			<section>
				<button type="button" class="row-btn" onclick={() => fileInput?.click()}>
					<ImageIcon size={16} />
					<span class="row-label">Add photo or file</span>
				</button>
			</section>

			<section>
				<h3>Focus</h3>
				{#each FOCUS_MODES as mode (mode.id)}
					<button type="button" class="row-btn" onclick={() => selectFocus(mode.id)}>
						<mode.icon size={16} />
						<span class="row-label">
							{mode.label}
							<span class="row-description">{mode.description}</span>
						</span>
						{#if focusMode === mode.id}<Check size={14} class="row-check" />{/if}
					</button>
				{/each}
			</section>

			<section>
				<h3>Deep Research</h3>
				<button type="button" class="row-btn" onclick={toggleDeepResearch}>
					<Microscope size={16} />
					<span class="row-label">
						Deep Research
						<span class="row-description">Digs further before answering — costs more, takes longer</span>
					</span>
					{#if deepResearch}<Check size={14} class="row-check" />{/if}
				</button>
			</section>

			<section>
				<h3>Model</h3>
				{#each appState.models as model (model.id)}
					<button type="button" class="row-btn" onclick={() => selectModel(model.id)}>
						<Cpu size={16} />
						<span class="row-label">{model.name}</span>
						{#if appState.selectedModel === model.id}<Check size={14} class="row-check" />{/if}
					</button>
				{/each}
			</section>
		</div>
	</div>
{/if}

<input bind:this={fileInput} type="file" accept="image/*,.pdf" hidden onchange={handleFileChange} />

<style>
	/* Doubles as a status readout — badges for whatever's actively turned
	   on, see activeFocusLabel above. */
	.trigger {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		max-width: 100%;
		border: 1px solid var(--color-border);
		background: var(--color-surface-2);
		border-radius: 999px;
		padding: 8px 12px 8px 10px;
		height: 38px;
		color: var(--color-text-dim);
		transition:
			border-color 0.15s var(--ease-out-expo),
			background-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo);
	}

	.trigger:hover {
		border-color: var(--color-border-strong);
		background: var(--color-surface-3);
		color: var(--color-text);
	}

	.trigger :global(svg:first-child) {
		flex-shrink: 0;
	}

	.trigger-label {
		font-size: 13px;
		color: var(--color-text);
	}

	.trigger-badge {
		flex-shrink: 0;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-size: 11px;
		font-weight: 600;
		color: var(--color-accent-2);
		background: color-mix(in srgb, var(--color-accent-2) 16%, transparent);
		border-radius: 999px;
		padding: 2px 8px;
	}

	.trigger-badge.deep {
		color: var(--color-accent);
		background: var(--color-accent-soft);
	}

	/* Everything below mirrors SettingsPanel.svelte's modal almost
	   verbatim (same class names, same values) — deliberately: one popup
	   treatment for the whole app, not a bottom sheet here and a centered
	   panel there. */
	.backdrop {
		position: fixed;
		inset: 0;
		z-index: 100;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 16px;
	}

	.backdrop-close {
		position: absolute;
		inset: 0;
		border: none;
		padding: 0;
		background: rgba(0, 0, 0, 0.62);
		backdrop-filter: blur(8px);
		-webkit-backdrop-filter: blur(8px);
		cursor: default;
	}

	.panel {
		position: relative;
		width: 100%;
		max-width: 440px;
		max-height: 85vh;
		overflow-y: auto;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-lg);
		box-shadow:
			0 32px 80px -20px rgba(0, 0, 0, 0.6),
			0 12px 32px -12px rgba(0, 0, 0, 0.45),
			0 0 0 1px rgba(0, 0, 0, 0.2);
		padding: 24px 24px 20px;
	}

	:root[data-theme='light'] .panel {
		box-shadow:
			0 32px 80px -20px rgba(50, 40, 28, 0.28),
			0 12px 32px -12px rgba(50, 40, 28, 0.18);
	}

	.panel-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 18px;
	}

	.panel-header h2 {
		margin: 0;
		font-family: var(--font-serif);
		font-size: 22px;
		font-weight: 700;
		letter-spacing: -0.005em;
	}

	/* .icon-btn itself is a global class (app.css) shared with the sidebar
	   toggle/settings/etc. — already resets border/background correctly,
	   no local override needed. */

	section {
		margin-bottom: 18px;
		padding-bottom: 18px;
		border-bottom: 1px solid var(--color-border);
	}

	section:last-child {
		border-bottom: none;
		margin-bottom: 0;
		padding-bottom: 0;
	}

	section h3 {
		margin: 0 0 12px 0;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.12em;
		color: var(--color-text);
	}

	.row-btn {
		display: flex;
		align-items: center;
		gap: 10px;
		width: 100%;
		border: none;
		background: transparent;
		border-radius: var(--radius-md);
		padding: 8px 6px;
		text-align: left;
		font-size: 14px;
		color: var(--color-text);
		transition: background-color 0.12s var(--ease-out-expo);
	}

	.row-btn:hover {
		background: var(--color-surface-3);
	}

	.row-btn :global(svg:first-child) {
		flex-shrink: 0;
		color: var(--color-text-dim);
	}

	.row-label {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}

	.row-description {
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.row-btn :global(.row-check) {
		flex-shrink: 0;
		color: var(--color-accent);
	}
</style>
