<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { pulsarDailyState, type PulsarDailyConfigInput } from '$lib/pulsarDaily.svelte';
	import { X, Sparkles, Plus, Trash2, Newspaper, PenLine, Cpu, CalendarClock } from '@lucide/svelte';
	import type { PulsarDailyCustomBlock } from '$lib/types';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { autoResize } from '$lib/actions/autoResize';
	import PulsarPromptWizard from './PulsarPromptWizard.svelte';

	// customFieldMaxHeight: roughly 5 lines at this field's font-size/line-
	// height — some of these instructions can get long (a multi-city
	// Local override, a detailed Sports team list), and a single-line
	// input would force that to scroll sideways instead of wrapping.
	const customFieldMaxHeight = 110;

	let { onClose }: { onClose: () => void } = $props();

	// blockOptions mirrors gateway/pulsar_daily.go's dailyBlockRegistry —
	// top_story deliberately excluded, same reasoning as the registry's
	// own doc comment: it's Stage B's elevation of whichever watch block
	// wins the ranking pass, not independently toggleable content.
	//
	// customizable/placeholder: every block except Weather (no "topic" to
	// steer — see the plan doc's location-only resolution for it) gets an
	// optional free-text field, added after real usage showed the
	// original v1 design — "sane defaults work, no per-block setting
	// earns its keep besides Sports" — was wrong. A generic "give me the
	// news" prompt with no way to say what you actually want to see isn't
	// useful even with a good default; Sports keeps its own required
	// field below since it has no sane default at all, not just a
	// generic one.
	const blockOptions = [
		{ key: 'word_of_day', label: 'Word of the Day', placeholder: 'e.g. favor scientific or literary words' },
		{ key: 'weather', label: 'Weather', placeholder: null },
		{ key: 'on_this_day', label: 'On This Day', placeholder: 'e.g. favor space/technology history' },
		{ key: 'quote', label: 'Quote of the Day', placeholder: 'e.g. favor quotes about creativity' },
		{ key: 'picture_of_day', label: 'Picture of the Day', placeholder: 'e.g. always space or wildlife photography' },
		{ key: 'headlines', label: 'Top Headlines', placeholder: 'e.g. focus on AI, geopolitics, or your interests' },
		{ key: 'trending', label: 'Trending Now', placeholder: 'e.g. focus on gaming and your hobbies' },
		{
			key: 'local',
			label: 'Local',
			placeholder: 'e.g. Beaverton, OR and also Portland, OR (a nearby major city)'
		},
		{ key: 'sports', label: 'Sports', placeholder: null }
	];

	const cfg = pulsarDailyState.config;
	// dailyEnabled: the whole-feature on/off switch — distinct from
	// enabledBlocks below, which picks which blocks run once Daily itself
	// is on. Defaults true so an existing config (created before this
	// field existed) reads as "was already running", matching the
	// backend's own column default — see store's enabled column comment.
	let dailyEnabled = $state(cfg?.enabled ?? true);
	let enabledBlocks = $state(new Set(cfg?.enabled_blocks ?? blockOptions.map((b) => b.key)));
	let sportsTeams = $state(cfg?.sports_teams ?? '');
	// customInstructions: keyed by block key, one entry per customizable
	// block above — pre-filled from any values already saved.
	let customInstructions = $state<Record<string, string>>({ ...(cfg?.custom_instructions ?? {}) });
	// customBlocks: user-authored "general purpose" blocks — see
	// store.PulsarDailyConfig.CustomBlocks' doc comment. A fresh copy of
	// each object (not the same references as cfg.custom_blocks) so
	// editing here doesn't mutate pulsarDailyState.config until Save.
	let customBlocks = $state<PulsarDailyCustomBlock[]>((cfg?.custom_blocks ?? []).map((b) => ({ ...b })));
	// weatherLocation overrides config.yaml's app-wide default_location
	// for Weather only — blank means "use default_location", same
	// fallback every other location-aware tool already has. Weather is
	// the one block that couldn't use customInstructions' "append a
	// steering sentence" mechanism at all (it has no LLM-authored task
	// text to append to — it's a direct tool dispatch), so it gets its
	// own dedicated field instead.
	let weatherLocation = $state(cfg?.weather_location ?? '');

	function addCustomBlock() {
		customBlocks.push({
			// crypto.randomUUID() (not a slug of the title) so renaming a
			// block later doesn't change its identity — see the store
			// type's doc comment on why the key has to stay stable.
			key: `custom_${crypto.randomUUID().slice(0, 8)}`,
			title: '',
			instructions: ''
		});
	}

	function removeCustomBlock(key: string) {
		customBlocks = customBlocks.filter((b) => b.key !== key);
	}

	let architectModel = $state(cfg?.architect_model ?? 'deepseek-pro');
	let writerModel = $state(cfg?.writer_model ?? 'deepseek');
	let timeOfDay = $state(cfg?.time_of_day ?? '07:00');

	let saving = $state(false);
	let error = $state('');

	// generateNow's own status — separate from saving/error above since
	// triggering a generation and saving config are unrelated actions
	// that can each fail independently without the modal conflating them.
	let generating = $state(false);
	let generateResult = $state('');

	async function generateNow() {
		if (generating) return;
		generating = true;
		generateResult = '';
		const result = await pulsarDailyState.generateNow();
		generating = false;
		generateResult = result.error || 'Started — check The Daily page in a few minutes.';
	}

	// wizardBlock: which block's "help me write this" interview is
	// currently open, if any — same one-at-a-time modal-over-modal shape
	// PulsarRoutineForm.svelte's own wizard button uses. isCustom
	// distinguishes a custom block's own full instructions field (written
	// to customBlocks) from a fixed registry block's short steer (written
	// to customInstructions) — different storage, different wizard system
	// prompt server-side (see gateway/pulsar_wizard.go's
	// IsCustomDailyBlock).
	let wizardBlock = $state<{ key: string; label: string; isCustom: boolean } | null>(null);

	function acceptWizardInstruction(text: string) {
		if (wizardBlock!.isCustom) {
			const block = customBlocks.find((b) => b.key === wizardBlock!.key);
			if (block) block.instructions = text;
		} else {
			customInstructions[wizardBlock!.key] = text;
		}
		wizardBlock = null;
	}

	function toggleBlock(key: string) {
		const next = new Set(enabledBlocks);
		if (next.has(key)) next.delete(key);
		else next.add(key);
		enabledBlocks = next;
	}

	async function submit(e: Event) {
		e.preventDefault();
		if (saving) return;
		saving = true;
		error = '';

		// Trimmed-empty entries are dropped rather than sent as "" — keeps
		// the persisted map free of noise from a field someone typed into
		// and then cleared.
		const trimmedInstructions: Record<string, string> = {};
		for (const [key, value] of Object.entries(customInstructions)) {
			const trimmed = value.trim();
			if (trimmed) trimmedInstructions[key] = trimmed;
		}

		// A half-filled row (title typed, instructions not, or vice versa)
		// is silently dropped rather than rejected with a validation error
		// — less surprising than blocking Save over a block someone hasn't
		// finished writing yet or decided against.
		const validCustomBlocks = customBlocks
			.map((b) => ({ key: b.key, title: b.title.trim(), instructions: b.instructions.trim() }))
			.filter((b) => b.title && b.instructions);

		const input: PulsarDailyConfigInput = {
			enabled_blocks: [...enabledBlocks],
			sports_teams: sportsTeams.trim(),
			custom_instructions: trimmedInstructions,
			custom_blocks: validCustomBlocks,
			weather_location: weatherLocation.trim(),
			architect_model: architectModel,
			writer_model: writerModel,
			time_of_day: timeOfDay,
			enabled: dailyEnabled
		};

		const result = await pulsarDailyState.updateConfig(input);
		saving = false;
		if (result.error) {
			error = result.error || 'Something went wrong — try again.';
			return;
		}
		onClose();
	}
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Configure The Daily">
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>The Daily settings</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		<form onsubmit={submit}>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label daily-enabled-label">The Daily</span>
					<label class="switch">
						<input type="checkbox" bind:checked={dailyEnabled} />
						<span class="slider"></span>
					</label>
				</div>
			</div>
			<p class="hint">
				{dailyEnabled
					? 'Generates automatically at the scheduled time below.'
					: "Off — won't generate on its own. \"Generate now\" still works."}
			</p>

			<div class="section-head"><Newspaper size={15} /><span class="section-title">Blocks</span></div>
			<div class="settings-group">
				{#each blockOptions as opt (opt.key)}
					{@const expanded =
						(opt.key === 'weather' || opt.key === 'sports' || opt.placeholder) &&
						enabledBlocks.has(opt.key)}
					<div class="settings-row" class:stacked={expanded}>
						<div class="block-toggle-row">
							<span class="row-label">{opt.label}</span>
							<label class="switch">
								<input
									type="checkbox"
									checked={enabledBlocks.has(opt.key)}
									onchange={() => toggleBlock(opt.key)}
								/>
								<span class="slider"></span>
							</label>
						</div>
						{#if opt.key === 'weather' && enabledBlocks.has('weather')}
							<div class="subfield">
								<label for="daily-weather-location">Location (optional)</label>
								<input
									id="daily-weather-location"
									type="text"
									class="stacked-input"
									bind:value={weatherLocation}
									placeholder="e.g. Seattle, WA — leave blank to use the server's default location"
								/>
							</div>
						{:else if opt.key === 'sports' && enabledBlocks.has('sports')}
							<div class="subfield">
								<div class="field-label-row">
									<label for="daily-sports-teams">Which teams/leagues?</label>
									<button
										type="button"
										class="wizard-btn"
										onclick={() => (wizardBlock = { key: opt.key, label: opt.label, isCustom: false })}
									>
										<Sparkles size={12} />
										Help me write this
									</button>
								</div>
								<textarea
									id="daily-sports-teams"
									class="stacked-input"
									rows="1"
									bind:value={sportsTeams}
									placeholder="Warriors, 49ers, Premier League"
									required
									use:autoResize={{ value: sportsTeams, maxHeight: customFieldMaxHeight }}
								></textarea>
							</div>
						{:else if opt.placeholder && enabledBlocks.has(opt.key)}
							<div class="subfield">
								<div class="field-label-row">
									<label for="daily-custom-{opt.key}">What do you want to see? (optional)</label>
									<button
										type="button"
										class="wizard-btn"
										onclick={() => (wizardBlock = { key: opt.key, label: opt.label, isCustom: false })}
									>
										<Sparkles size={12} />
										Help me write this
									</button>
								</div>
								<textarea
									id="daily-custom-{opt.key}"
									class="stacked-input"
									rows="1"
									value={customInstructions[opt.key] ?? ''}
									oninput={(e) => (customInstructions[opt.key] = e.currentTarget.value)}
									placeholder={opt.placeholder}
									use:autoResize={{ value: customInstructions[opt.key] ?? '', maxHeight: customFieldMaxHeight }}
								></textarea>
							</div>
						{/if}
					</div>
				{/each}
			</div>

			<div class="section-label-row">
				<div class="section-head"><PenLine size={15} /><span class="section-title">Custom blocks</span></div>
				<button type="button" class="wizard-btn" onclick={addCustomBlock}>
					<Plus size={12} />
					New general purpose block
				</button>
			</div>
			{#if customBlocks.length === 0}
				<p class="hint">
					Anything outside the built-in set — a stock watchlist, a specific hobby, a running project
					you want tracked. Runs with the same research tools as Headlines or Local.
				</p>
			{:else}
				<div class="settings-group">
					{#each customBlocks as block (block.key)}
						<div class="settings-row stacked">
							<div class="field-label-row">
								<input
									type="text"
									class="custom-block-title"
									value={block.title}
									oninput={(e) => (block.title = e.currentTarget.value)}
									placeholder="Title, e.g. Stock Watchlist"
								/>
								<button
									type="button"
									class="wizard-btn"
									onclick={() =>
										(wizardBlock = {
											key: block.key,
											label: block.title || 'this block',
											isCustom: true
										})}
								>
									<Sparkles size={12} />
									Help me write this
								</button>
								<button
									type="button"
									class="icon-btn"
									onclick={() => removeCustomBlock(block.key)}
									title="Remove"
									aria-label="Remove {block.title || 'this custom block'}"
								>
									<Trash2 size={14} />
								</button>
							</div>
							<textarea
								class="stacked-input"
								rows="1"
								value={block.instructions}
								oninput={(e) => (block.instructions = e.currentTarget.value)}
								placeholder="What should this check every day? e.g. Look up today's closing prices for NVDA and AAPL and report them."
								use:autoResize={{ value: block.instructions, maxHeight: customFieldMaxHeight }}
							></textarea>
						</div>
					{/each}
				</div>
			{/if}

			<div class="section-head"><Cpu size={15} /><span class="section-title">Models</span></div>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label">Architect</span>
					<select bind:value={architectModel}>
						{#each appState.models as m (m.id)}
							<option value={m.id}>{m.name}</option>
						{/each}
					</select>
				</div>
				<div class="settings-row">
					<span class="row-label">Writer</span>
					<select bind:value={writerModel}>
						{#each appState.models as m (m.id)}
							<option value={m.id}>{m.name}</option>
						{/each}
					</select>
				</div>
			</div>
			<p class="hint">
				Architect judges what changed and elects today's Top Story — Stage A/B's decisions. Writer
				writes every block's actual content — Stage A/C's prose.
			</p>

			<div class="section-head"><CalendarClock size={15} /><span class="section-title">Schedule</span></div>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label">Generates at</span>
					<input type="time" bind:value={timeOfDay} />
				</div>
				<div class="settings-row">
					<span class="row-label">Right now</span>
					<button type="button" class="btn generate-now-btn" onclick={generateNow} disabled={generating}>
						{generating ? 'Starting…' : 'Generate now'}
					</button>
				</div>
			</div>
			<p class="hint">Server-local time — no timezone handling.</p>
			<p class="hint">
				{generateResult || 'Runs today’s edition immediately with the currently-saved settings, not whatever’s still unsaved in this form.'}
			</p>

			{#if error}
				<p class="error">{error}</p>
			{/if}

			<div class="actions">
				<button type="submit" class="btn btn-accent save-btn" disabled={saving}>
					{saving ? 'Saving…' : 'Save'}
				</button>
			</div>
		</form>
	</div>
</div>

{#if wizardBlock}
	<PulsarPromptWizard
		seed={wizardBlock.isCustom
			? (customBlocks.find((b) => b.key === wizardBlock!.key)?.instructions ?? '')
			: (customInstructions[wizardBlock.key] ?? '')}
		dailyBlockTitle={wizardBlock.label}
		isCustomBlock={wizardBlock.isCustom}
		onClose={() => (wizardBlock = null)}
		onAccept={acceptWizardInstruction}
	/>
{/if}

<style>
	/* Card-grouped row pattern, matching ConstellationSettingsModal.svelte
	   exactly (issue #83's settings-menu unification). Duplicated per-file
	   for the same reason .switch below is — Svelte scopes component
	   styles, so there's no shared-import version of this. */
	/* See SettingsPanel.svelte's .section-head comment — same gold-icon
	   + rule treatment, replacing the old all-dim .section-label. */
	.section-head {
		display: flex;
		align-items: center;
		gap: 9px;
		margin-top: var(--space-2xl);
		padding-bottom: var(--space-xs);
		margin-bottom: var(--space-xs);
		border-bottom: 1px solid var(--color-border);
	}
	.section-head :global(svg) {
		color: var(--color-accent);
		flex-shrink: 0;
	}
	.section-title {
		font-size: 15px;
		font-weight: 600;
		color: var(--color-text);
	}

	.settings-group {
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		overflow: hidden;
		/* Tight on purpose — see SettingsPanel.svelte's identical comment. */
		margin-bottom: var(--space-xs);
	}

	.settings-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border);
		font-size: 14px;
	}

	.settings-row:last-child {
		border-bottom: none;
	}

	/* A block row with an inline sub-field (weather's location, sports'
	   required team list, or any block with a custom-instructions
	   placeholder) stacks its toggle above the field instead of trying
	   to fit both on one line. */
	.settings-row.stacked {
		flex-direction: column;
		align-items: stretch;
	}

	.row-label {
		font-size: 14px;
		font-weight: 500;
	}

	.block-toggle-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		width: 100%;
	}

	/* A sub-field lives inside its own block's row (dashed divider, not a
	   separate card) so the toggle and the field it controls read as one
	   unit while scanning down a long block list — see the mockup this
	   was built from at mockups/settings-unification.html. */
	.subfield {
		width: 100%;
		margin-top: var(--space-md);
		padding-top: var(--space-md);
		border-top: 1px dashed var(--color-border);
	}

	.subfield label {
		display: block;
		margin-bottom: var(--space-xs);
		font-size: 12px;
		color: var(--color-text-dim);
	}

	/* The rule lives on this outer row (spanning icon + title + the trailing
	   "New block" button) rather than on .section-head itself — .section-head
	   loses its own border/margin here so there's one rule under the whole
	   row, not two competing ones. */
	.section-label-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		margin-top: var(--space-2xl);
		padding-bottom: var(--space-xs);
		margin-bottom: var(--space-xs);
		border-bottom: 1px solid var(--color-border);
	}
	.section-label-row .section-head {
		margin-top: 0;
		padding-bottom: 0;
		margin-bottom: 0;
		border-bottom: none;
	}

	.custom-block-title {
		flex: 1;
		border: none;
		background: transparent;
		font: inherit;
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text);
		padding: var(--space-xs) 0;
	}
	.custom-block-title::placeholder {
		font-weight: 400;
		color: var(--color-text-dim);
	}

	.field-label-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.field-label-row label {
		margin-bottom: 0;
	}

	/* Same wizard-launch button as PulsarRoutineForm.svelte's prompt
	   field — duplicated, not shared, since Svelte scopes component
	   styles per-file (see that component's own doc comment on this
	   convention elsewhere). */
	.wizard-btn {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		margin-bottom: var(--space-xs);
		padding: 2px var(--space-sm);
		border: none;
		background: transparent;
		border-radius: var(--radius-full);
		font-size: 11.5px;
		font-weight: 600;
		color: var(--color-accent);
	}

	.wizard-btn:hover {
		background: var(--color-accent-soft);
	}

	/* Full-width, "carved into the surface" field — used for every
	   sub-field inside a stacked settings-row (weather location, sports
	   teams, headlines steer, custom block instructions). */
	.stacked-input {
		width: 100%;
		border: none;
		background: var(--color-surface-3);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
		font: inherit;
		font-size: 13px;
		color: var(--color-text);
		resize: none;
	}

	.settings-row select,
	.settings-row input[type='time'] {
		border: 1px solid var(--color-border);
		background: var(--color-surface-3);
		border-radius: var(--radius-md);
		padding: var(--space-xs) var(--space-sm);
		font-size: 13px;
		color: var(--color-text);
	}

	.hint {
		margin: 0 0 var(--space-md);
		font-size: 12px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	.error {
		margin: 0 0 var(--space-md);
		font-size: 12.5px;
		color: var(--color-danger);
	}

	.actions {
		display: flex;
		justify-content: flex-end;
		margin-top: var(--space-lg);
	}

	.save-btn {
		margin-left: auto;
	}

	.generate-now-btn {
		font-size: 13px;
		padding: var(--space-sm) var(--space-md);
	}

	.daily-enabled-label {
		font-weight: 600;
	}

	/* Same switch construction as PulsarRoutineForm.svelte/
	   SettingsPanel.svelte/ComposerMenu.svelte — duplicated, not shared,
	   since Svelte scopes component styles per-file. */
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
		border-radius: var(--radius-full);
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
		transition:
			transform 0.15s ease,
			background 0.15s ease;
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
