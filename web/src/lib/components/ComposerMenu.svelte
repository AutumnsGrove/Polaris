<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import type { FocusMode } from '$lib/types';
	import { FOCUS_MODES } from '$lib/focusModes';
	import { Plus, Image as ImageIcon, Cpu, Microscope, Check, X, ChevronLeft, ChevronRight, SlidersHorizontal } from '@lucide/svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';

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

	// Root list of rows -> drill into a picker screen for the two things
	// that are actually a *list* (Focus, Model). Showing all five focus
	// modes with their full descriptions AND the whole model list flat, all
	// at once, made the sheet read as a wall of text — most of a screen's
	// worth of copy for options that are picked once and rarely revisited.
	// The root now shows one line per category with its current value, and
	// only the category actually being changed expands.
	let view = $state<'root' | 'focus' | 'model'>('root');
	// Slide direction for the {#key view} transition below — forward into a
	// picker, backward out of one — so the motion itself reads as "going
	// deeper" vs. "coming back", not just a generic cross-fade.
	let direction = $state(1);

	function reset() {
		view = 'root';
		direction = 1;
	}

	function close() {
		open = false;
		reset();
	}

	function drillInto(next: 'focus' | 'model') {
		direction = 1;
		view = next;
	}

	function backToRoot() {
		direction = -1;
		view = 'root';
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
	let selectedModelName = $derived(appState.models.find((m) => m.id === appState.selectedModel)?.name ?? '');

	let headerTitle = $derived(view === 'focus' ? 'Focus' : view === 'model' ? 'Model' : 'More');
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
	<div class="modal-backdrop" role="presentation">
		<button class="modal-backdrop-close" onclick={close} aria-label="Close"></button>
		<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Composer options">
			<div class="sheet-handle" use:swipeToDismiss={close} aria-hidden="true"></div>
			<div class="modal-panel-header">
				<div class="header-title">
					{#if view !== 'root'}
						<button class="icon-btn back-btn" onclick={backToRoot} title="Back">
							<ChevronLeft size={18} />
						</button>
					{/if}
					<h2>{headerTitle}</h2>
				</div>
				<button class="icon-btn" onclick={close} title="Close"><X size={18} /></button>
			</div>

			<div class="view-clip">
				{#key view}
					<div class="view" in:fly={{ x: direction * 28, duration: 180, easing: quintOut }}>
						{#if view === 'root'}
							<section>
								<button type="button" class="row-btn" onclick={() => fileInput?.click()}>
									<ImageIcon size={16} />
									<span class="row-label">Add photo or file</span>
								</button>

								<button type="button" class="row-btn" onclick={() => drillInto('focus')}>
									<SlidersHorizontal size={16} />
									<span class="row-label">Focus</span>
									<span class="row-value">{activeFocusLabel ?? 'Off'}</span>
									<ChevronRight size={14} class="row-chevron" />
								</button>

								<div class="row-btn row-static">
									<Microscope size={16} />
									<span class="row-label">
										Deep Research
										<span class="row-description">Digs further before answering — costs more, takes longer</span>
									</span>
									<label class="switch">
										<input type="checkbox" checked={deepResearch} onchange={toggleDeepResearch} />
										<span class="slider"></span>
									</label>
								</div>

								<button type="button" class="row-btn" onclick={() => drillInto('model')}>
									<Cpu size={16} />
									<span class="row-label">Model</span>
									<span class="row-value">{selectedModelName}</span>
									<ChevronRight size={14} class="row-chevron" />
								</button>
							</section>
						{:else if view === 'focus'}
							<section>
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
						{:else if view === 'model'}
							<section>
								{#each appState.models as model (model.id)}
									<button type="button" class="row-btn" onclick={() => selectModel(model.id)}>
										<Cpu size={16} />
										<span class="row-label">{model.name}</span>
										{#if appState.selectedModel === model.id}<Check size={14} class="row-check" />{/if}
									</button>
								{/each}
							</section>
						{/if}
					</div>
				{/key}
			</div>
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
		border: none;
		background: var(--color-surface-2);
		border-radius: 999px;
		padding: 8px 12px 8px 10px;
		height: 38px;
		color: var(--color-text-dim);
		box-shadow: var(--shadow-xs);
		transition:
			background-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo),
			box-shadow 0.15s var(--ease-out-expo);
	}

	.trigger:hover {
		background: var(--color-surface-3);
		color: var(--color-text);
		box-shadow: var(--shadow-sm);
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

	/* .modal-backdrop/.modal-panel/.modal-panel-header live in app.css —
	   shared with SettingsPanel.svelte, one popup treatment (including
	   the mobile bottom-sheet behavior) for the whole app instead of two
	   copies to keep in sync by hand. */

	/* .icon-btn itself is a global class (app.css) shared with the sidebar
	   toggle/settings/etc. — already resets border/background correctly,
	   no local override needed. */

	.header-title {
		display: flex;
		align-items: center;
		gap: 4px;
	}

	.back-btn {
		margin-left: -6px;
	}

	/* Clips the sliding root/focus/model views to the header's width so a
	   view mid-transition doesn't spill past the panel's rounded corners
	   or trigger a horizontal scrollbar. Height isn't fixed — the panel's
	   own max-height + overflow-y (see app.css) handles a picker screen
	   that's taller than the root list. */
	.view-clip {
		position: relative;
		overflow-x: hidden;
	}

	.view {
		width: 100%;
	}

	section {
		margin-bottom: 18px;
		padding-bottom: 18px;
	}

	section:last-child {
		margin-bottom: 0;
		padding-bottom: 0;
	}

	.row-btn {
		display: flex;
		align-items: center;
		gap: 10px;
		width: 100%;
		border: none;
		background: transparent;
		border-radius: var(--radius-md);
		padding: 10px 6px;
		text-align: left;
		font-size: 14px;
		color: var(--color-text);
		transition: background-color 0.12s var(--ease-out-expo);
	}

	.row-btn:hover {
		background: var(--color-surface-3);
	}

	/* The Deep Research row is a plain div, not a button — the switch is
	   the only interactive element in it (same shape as SettingsPanel's
	   own toggle rows), so it shouldn't hover-highlight or show a pointer
	   cursor as if the whole row were clickable. */
	.row-btn.row-static {
		cursor: default;
	}

	.row-btn.row-static:hover {
		background: transparent;
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

	/* The current value shown on a root row that drills into a picker
	   (Focus, Model) — muted so it reads as "here's what's set", not as a
	   second competing label next to the row's own name. */
	.row-value {
		flex-shrink: 0;
		max-width: 120px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-size: 13px;
		color: var(--color-text-dim);
	}

	.row-btn :global(.row-chevron) {
		flex-shrink: 0;
		color: var(--color-text-dim);
	}

	/* Same switch as SettingsPanel.svelte's — duplicated rather than
	   shared since Svelte scopes component styles per-file, but it's the
	   same visual vocabulary everywhere a boolean setting appears, not a
	   bespoke one just for this row (see the Deep Research row above). */
	.switch {
		position: relative;
		display: inline-block;
		width: 36px;
		height: 20px;
		flex-shrink: 0;
	}

	.switch input {
		opacity: 0;
		width: 0;
		height: 0;
	}

	.slider {
		position: absolute;
		inset: 0;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: 999px;
		cursor: pointer;
		transition: background 0.15s ease;
	}

	.slider::before {
		content: '';
		position: absolute;
		width: 14px;
		height: 14px;
		left: 2px;
		top: 2px;
		background: var(--color-text-dim);
		border-radius: 50%;
		transition: transform 0.15s ease, background 0.15s ease;
	}

	.switch input:checked + .slider {
		background: color-mix(in srgb, var(--color-accent) 30%, transparent);
		border-color: var(--color-accent);
	}

	.switch input:checked + .slider::before {
		transform: translateX(16px);
		background: var(--color-accent);
	}
</style>
