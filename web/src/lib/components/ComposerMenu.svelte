<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import type { FocusMode } from '$lib/types';
	import { FOCUS_MODES } from '$lib/focusModes';
	import { Plus, Image as ImageIcon, Cpu, Microscope, Globe, Check, X, ChevronLeft, ChevronRight, SlidersHorizontal, Ban } from '@lucide/svelte';
	import Asterism from './Asterism.svelte';
	import FieldIcon from './FieldIcon.svelte';
	import Switch from './Switch.svelte';
	import { fieldsState, fieldColorVar } from '$lib/fields.svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';
	import type { ModelPricing } from '$lib/types';

	function formatPricing(pricing: ModelPricing | undefined): string {
		if (!pricing) return '';
		if (pricing.prompt_per_m === 0 && pricing.completion_per_m === 0) return 'Free';
		return `$${pricing.prompt_per_m} / $${pricing.completion_per_m} per M`;
	}

	// Everything that used to be separate controls (model picker, focus
	// modes, deep research, attach) is consolidated into one "+"-triggered
	// popup — mobile is the primary surface here (see PRODUCT.md), and a
	// row of five-plus small buttons across the composer doesn't survive
	// phone width. Same centered-modal pattern as SettingsPanel.svelte
	// (not a distinct bottom-sheet component) so the app has exactly one
	// popup treatment, not two competing ones.
	let {
		focusMode = $bindable<FocusMode>('off'),
		focusModeManual = $bindable(false),
		deepResearch = $bindable(false),
		research = $bindable(true),
		ghostMode = false,
		onAttach
	}: {
		focusMode: FocusMode;
		// See ChatView.svelte's focusModeManual doc comment — set true only
		// by selectFocus below, a live tap in this picker, so Oracle mode
		// (docs/plans/oracle-mode.md) can tell "the operator picked this for
		// this specific message" from "focusMode is just the standing
		// default".
		focusModeManual: boolean;
		deepResearch: boolean;
		// The composer's "Research" toggle — on by default, same shape as
		// deepResearch but inverted: turning it OFF is what enables chat
		// mode (see tools.Context.NoResearch), not the other way around,
		// so a caller that never wires this prop up still gets normal
		// research behavior rather than accidentally starting in chat mode.
		research: boolean;
		// The composer's ghost toggle (ChatView.svelte's ghostMode) — the
		// Oracle mark/reading UI stays dark when it's on and the
		// oracle_ghost_enabled opt-in is off, since Oracle won't run. See
		// oracleActive below.
		ghostMode?: boolean;
		onAttach: (files: File[]) => void;
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
	let view = $state<'root' | 'focus' | 'field' | 'model'>('root');
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

	function drillInto(next: 'focus' | 'field' | 'model') {
		direction = 1;
		view = next;
		// The sidebar already loads this at startup, but a Field created in
		// another tab since then would be missing from the list otherwise.
		if (next === 'field') void fieldsState.load();
	}

	function backToRoot() {
		direction = -1;
		view = 'root';
	}

	function selectFocus(id: FocusMode) {
		// Tapping the already-active mode turns it back off — a toggle,
		// same shape as AudioPlayer's readAloud-the-active-turn pattern.
		focusMode = focusMode === id ? 'off' : id;
		focusModeManual = true;
		void appState.persistThreadConfig(appState.selectedModel, focusMode, deepResearch, !research);
		close();
	}

	// Filing under a Field is a per-thread fact, not a per-message setting
	// like focus: an open thread is moved for real (PUT /api/threads/{id}/
	// field), a not-yet-created one just stages it. Left open on failure with
	// the server's message shown, same as ThreadMenu's move picker — a sheet
	// that vanishes reads as the tap having done nothing.
	let fieldError = $state('');
	async function selectField(id: string | null) {
		fieldError = '';
		const next = appState.activeFieldId === id ? null : id;
		const err = await appState.setThreadField(next);
		if (err) {
			fieldError = err;
			return;
		}
		// A brand-new thread inherits the Field's default focus mode, same
		// seeding ChatView's config effect does for startThreadInField —
		// unless a manual pick this session already beat it.
		if (appState.currentThreadId === null && !focusModeManual) {
			const fieldFocus = fieldsState.byId(next)?.default_focus_mode;
			focusMode = fieldFocus ? fieldFocus : appState.settings.defaultFocusMode;
		}
		close();
	}

	function selectModel(id: string) {
		appState.selectedModel = id;
		void appState.persistThreadConfig(id, focusMode, deepResearch, !research);
		close();
	}

	function toggleDeepResearch() {
		// Deep research implies research — can't dig further with the web/
		// tools access it depends on switched off. The row is also disabled
		// below while chat mode is on, but guard here too since this is the
		// state that actually reaches the backend (tools.Context.NoResearch).
		if (!research) return;
		deepResearch = !deepResearch;
		void appState.persistThreadConfig(appState.selectedModel, focusMode, deepResearch, !research);
	}

	function toggleResearch() {
		research = !research;
		// Turning research off pulls deep research down with it — deep
		// research with no research is a contradiction (see toggleDeepResearch),
		// and leaving it "on" here would let both badges show at once.
		if (!research) deepResearch = false;
		// Chat mode (research off) used to be composer-local only — leaving
		// a thread in chat mode and reopening it silently reset back to
		// research-on, since nothing here ever wrote it through. Persisted
		// the same way focus mode/deep research already are.
		void appState.persistThreadConfig(appState.selectedModel, focusMode, deepResearch, !research);
	}

	function handleFileChange(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		if (input.files?.length) onAttach(Array.from(input.files));
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

	// Oracle is on AND will actually read this turn — the composer's own
	// ghost toggle narrows AppState.oracleWillRun further for the
	// not-yet-sent case (oracleWillRun only sees an in-flight turn and the
	// open thread, and a ghost aimed at a brand-new thread has neither yet).
	// A ghost conversation Oracle is going to skip must not advertise
	// itself with the asterisk or the reading shimmer.
	let oracleActive = $derived(
		appState.oracleWillRun && (!ghostMode || appState.settings.oracleGhostEnabled)
	);

	// Oracle mode's own trigger state (docs/plans/oracle-mode.md's B12) —
	// when Oracle is on (see SettingsPanel.svelte's toggle) the pill takes a
	// faint gold tint and a small star at its right edge; the Plus stays
	// where it always was. "reading" sweeps a soft shimmer across the pill
	// for the window between sending a turn and Oracle finishing its
	// pre-read. Chosen from mockups/oracle-trigger-options.html (option E).
	//
	// That window now has a real end-of-signal: the early 'oracle' event
	// (turn.oracleResolved, see its doc comment) lands the moment Oracle
	// actually resolves — typically a couple of seconds in, and often many
	// seconds before the answer's first token — so keying only off "nothing
	// has streamed yet" used to keep the shimmer going after Oracle was
	// already done. unconfigured/over budget still falls back to the first
	// real output (no event will ever arrive); oracleActive above is what
	// keeps it off entirely for a turn Oracle is going to skip (ghost mode
	// without the opt-in).
	let oracleReading = $derived.by(() => {
		if (!appState.busy || !oracleActive) return false;
		// The last pushed turn while busy is always the pending assistant
		// reply (dispatch() pushes user-then-assistant as a pair) —
		// pendingTurn itself is private to AppState, so this reads the same
		// live object via the public turns array instead.
		const turn = appState.turns[appState.turns.length - 1];
		return (
			turn?.role === 'assistant' && !turn.oracleResolved && !turn.timeline?.length && !turn.content
		);
	});
	let activeField = $derived(fieldsState.byId(appState.activeFieldId));
	let selectedModelName = $derived(appState.models.find((m) => m.id === appState.selectedModel)?.name ?? '');

	let headerTitle = $derived(view === 'focus' ? 'Focus' : view === 'field' ? 'Field' : view === 'model' ? 'Model' : 'More');
</script>

<button
	type="button"
	class="trigger"
	class:oracle={oracleActive}
	class:reading={oracleReading}
	onclick={() => (open = true)}
	aria-label="Attach, focus modes, and model"
>
	<Plus size={16} />
	<span class="trigger-label">More</span>
	{#if activeFocusLabel}
		<span class="trigger-badge">{activeFocusLabel}</span>
	{/if}
	{#if activeField && !appState.isGhostThread}
		<span class="trigger-badge field">{activeField.name}</span>
	{/if}
	{#if deepResearch}
		<span class="trigger-badge deep">Deep research</span>
	{/if}
	{#if !research}
		<span class="trigger-badge chat">Chat mode</span>
	{/if}
	{#if oracleActive}
		<span class="oracle-mark" aria-hidden="true"><Asterism size={14} /></span>
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
									<span class="row-label">Add photos or files</span>
								</button>

								<button type="button" class="row-btn" onclick={() => drillInto('focus')}>
									<SlidersHorizontal size={16} />
									<span class="row-label">Focus</span>
									<span class="row-value">{activeFocusLabel ?? 'Off'}</span>
									<ChevronRight size={14} class="row-chevron" />
								</button>

								<!-- Ghost threads never join a Field (the server ignores it),
								     so the row would promise something that can't happen. -->
								{#if !appState.isGhostThread}
									<button type="button" class="row-btn" onclick={() => drillInto('field')}>
										<FieldIcon size={16} />
										<span class="row-label">Field</span>
										<span class="row-value">{activeField?.name ?? 'None'}</span>
										<ChevronRight size={14} class="row-chevron" />
									</button>
								{/if}

								<div class="row-btn row-static" class:row-disabled={!research}>
									<Microscope size={16} />
									<span class="row-label">
										Deep Research
										<span class="row-description">
											{research
												? 'Digs further before answering — costs more, takes longer'
												: 'Needs Research turned on'}
										</span>
									</span>
									<Switch
										label="Deep Research"
										checked={deepResearch}
										disabled={!research}
										onchange={toggleDeepResearch}
									/>
								</div>

								<div class="row-btn row-static">
									<Globe size={16} />
									<span class="row-label">
										Research
										<span class="row-description">Search the web and other tools — turn off for a plain chat</span>
									</span>
									<Switch label="Research" checked={research} onchange={toggleResearch} />
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
						{:else if view === 'field'}
							<section>
								<button type="button" class="row-btn" onclick={() => selectField(null)}>
									<Ban size={16} />
									<span class="row-label">
										No Field
										<span class="row-description">An ordinary conversation, on its own</span>
									</span>
									{#if !appState.activeFieldId}<Check size={14} class="row-check" />{/if}
								</button>
								{#each fieldsState.fields as field (field.id)}
									<button type="button" class="row-btn" onclick={() => selectField(field.id)}>
										<span
											class="field-dot"
											style:background={fieldColorVar(field.color) ?? 'var(--color-text-dim)'}
											aria-hidden="true"
										></span>
										<span class="row-label">
											{field.name}
											{#if field.description}<span class="row-description">{field.description}</span>{/if}
										</span>
										{#if appState.activeFieldId === field.id}<Check size={14} class="row-check" />{/if}
									</button>
								{/each}
								{#if fieldError}
									<p class="field-note error">{fieldError}</p>
								{:else if fieldsState.error && !fieldsState.loaded}
									<p class="field-note">
										Couldn't load your Fields.
										<button type="button" class="field-retry" onclick={() => fieldsState.load()}>Retry</button>
									</p>
								{:else if fieldsState.loaded && fieldsState.fields.length === 0}
									<p class="field-note">No Fields yet — create one from the Fields page.</p>
								{/if}
							</section>
						{:else if view === 'model'}
							<section>
								{#each appState.models as model (model.id)}
									<button type="button" class="row-btn" onclick={() => selectModel(model.id)}>
										<Cpu size={16} />
										<span class="row-label">
											{model.name}
											{#if model.pricing}
												<span class="row-description">{formatPricing(model.pricing)}</span>
											{/if}
										</span>
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

<input
	bind:this={fileInput}
	type="file"
	multiple
	accept="image/*,.pdf,.md,.txt,.json,.csv,.yaml,.yml,.xml"
	hidden
	onchange={handleFileChange}
/>

<style>
	/* Doubles as a status readout — badges for whatever's actively turned
	   on, see activeFocusLabel above. */
	.trigger {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		min-width: 0;
		max-width: 100%;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-full);
		padding: var(--space-sm) var(--space-md) var(--space-sm) var(--space-md);
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

	/* Oracle mode on: the whole pill is faintly gold-tinted and carries a
	   star at its right edge (docs/plans/oracle-mode.md's B12; picked from
	   mockups/oracle-trigger-options.html, option E). The earlier
	   always-on rainbow ring read as decoration, not status. */
	.trigger.oracle {
		position: relative;
		overflow: hidden;
		background: color-mix(in srgb, var(--color-accent) 12%, var(--color-surface-2));
	}

	.trigger.oracle:hover {
		background: color-mix(in srgb, var(--color-accent) 16%, var(--color-surface-3));
	}

	.oracle-mark {
		display: grid;
		place-items: center;
		flex-shrink: 0;
		color: var(--color-accent);
	}

	/* While Oracle is still pre-reading: a soft band of light crossing the
	   pill. pointer-events off so it never swallows the tap. */
	.trigger.oracle.reading::after {
		content: '';
		position: absolute;
		inset: 0;
		pointer-events: none;
		background: linear-gradient(
			100deg,
			transparent 30%,
			color-mix(in srgb, var(--color-accent) 22%, transparent) 50%,
			transparent 70%
		);
		transform: translateX(-100%);
		animation: oracle-shimmer 1.8s ease-in-out infinite;
	}

	@keyframes oracle-shimmer {
		to {
			transform: translateX(100%);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.trigger.oracle.reading::after {
			animation: none;
			transform: none;
			opacity: 0.6;
		}
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
		border-radius: var(--radius-full);
		padding: var(--space-xs) var(--space-sm);
	}

	.trigger-badge.deep {
		color: var(--color-accent);
		background: var(--color-accent-soft);
	}

	/* A long Field name must not push the other badges off a phone-width
	   pill — flex-shrink lets it truncate instead. */
	.trigger-badge.field {
		flex-shrink: 1;
		max-width: 9em;
		color: var(--color-text);
		background: var(--color-surface-3);
	}

	/* Distinct from .deep — this is "something's turned OFF", not a boost,
	   so it reads as a neutral/dimmer notice rather than the accent color
	   used for an active enhancement. */
	.trigger-badge.chat {
		color: var(--color-text-dim);
		background: var(--color-surface-3);
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
		gap: var(--space-xs);
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
		margin-bottom: var(--space-lg);
		padding-bottom: var(--space-lg);
	}

	section:last-child {
		margin-bottom: 0;
		padding-bottom: 0;
	}

	.row-btn {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		width: 100%;
		border: none;
		background: transparent;
		border-radius: var(--radius-md);
		padding: var(--space-md) var(--space-sm);
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

	/* Deep Research while chat mode (Research off) is on — same
	   opacity/pointer-events dim as SettingsPanel's .section-disabled, since
	   the checkbox's own disabled attribute (see markup above) already
	   blocks keyboard/screen-reader interaction; this just makes it *look*
	   locked too. */
	.row-btn.row-disabled {
		opacity: 0.45;
		pointer-events: none;
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

	/* The picker's color dot stands in for a row icon, sized to the 16px
	   glyph column the other rows use so labels stay aligned. */
	.field-dot {
		width: 10px;
		height: 10px;
		margin: 0 3px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.field-note {
		margin: var(--space-sm) var(--space-md);
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.field-note.error {
		color: var(--color-danger);
	}

	.field-retry {
		border: none;
		background: transparent;
		color: var(--color-accent);
		font: inherit;
		cursor: pointer;
	}
</style>
