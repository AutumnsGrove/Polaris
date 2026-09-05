<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { pulsarDailyState, type PulsarDailyConfigInput } from '$lib/pulsarDaily.svelte';
	import { X, Sparkles } from '@lucide/svelte';
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
		{ key: 'tech_science', label: 'Tech & Science', placeholder: 'e.g. focus on AI, robotics, biotech' },
		{
			key: 'local',
			label: 'Local',
			placeholder: 'e.g. Beaverton, OR and also Portland, OR (a nearby major city)'
		},
		{ key: 'sports', label: 'Sports', placeholder: null }
	];

	const cfg = pulsarDailyState.config;
	let enabledBlocks = $state(new Set(cfg?.enabled_blocks ?? blockOptions.map((b) => b.key)));
	let sportsTeams = $state(cfg?.sports_teams ?? '');
	// customInstructions: keyed by block key, one entry per customizable
	// block above — pre-filled from any values already saved.
	let customInstructions = $state<Record<string, string>>({ ...(cfg?.custom_instructions ?? {}) });
	let architectModel = $state(cfg?.architect_model ?? 'deepseek-pro');
	let writerModel = $state(cfg?.writer_model ?? 'deepseek');
	let timeOfDay = $state(cfg?.time_of_day ?? '07:00');

	let saving = $state(false);
	let error = $state('');

	// wizardBlock: which block's "help me write this" interview is
	// currently open, if any — same one-at-a-time modal-over-modal shape
	// PulsarRoutineForm.svelte's own wizard button uses.
	let wizardBlock = $state<{ key: string; label: string } | null>(null);

	function acceptWizardInstruction(text: string) {
		customInstructions[wizardBlock!.key] = text;
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

		const input: PulsarDailyConfigInput = {
			enabled_blocks: [...enabledBlocks],
			sports_teams: sportsTeams.trim(),
			custom_instructions: trimmedInstructions,
			architect_model: architectModel,
			writer_model: writerModel,
			time_of_day: timeOfDay
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
			<h3>Blocks</h3>
			<div class="block-list">
				{#each blockOptions as opt (opt.key)}
					<label class="block-row">
						<input
							type="checkbox"
							checked={enabledBlocks.has(opt.key)}
							onchange={() => toggleBlock(opt.key)}
						/>
						<span>{opt.label}</span>
					</label>
					{#if opt.key === 'sports' && enabledBlocks.has('sports')}
						<div class="field block-subfield">
							<div class="field-label-row">
								<label for="daily-sports-teams">Which teams/leagues?</label>
								<button
									type="button"
									class="wizard-btn"
									onclick={() => (wizardBlock = { key: opt.key, label: opt.label })}
								>
									<Sparkles size={12} />
									Help me write this
								</button>
							</div>
							<textarea
								id="daily-sports-teams"
								rows="1"
								bind:value={sportsTeams}
								placeholder="Warriors, 49ers, Premier League"
								required
								use:autoResize={{ value: sportsTeams, maxHeight: customFieldMaxHeight }}
							></textarea>
						</div>
					{:else if opt.placeholder && enabledBlocks.has(opt.key)}
						<div class="field block-subfield">
							<div class="field-label-row">
								<label for="daily-custom-{opt.key}">What do you want to see? (optional)</label>
								<button
									type="button"
									class="wizard-btn"
									onclick={() => (wizardBlock = { key: opt.key, label: opt.label })}
								>
									<Sparkles size={12} />
									Help me write this
								</button>
							</div>
							<textarea
								id="daily-custom-{opt.key}"
								rows="1"
								value={customInstructions[opt.key] ?? ''}
								oninput={(e) => (customInstructions[opt.key] = e.currentTarget.value)}
								placeholder={opt.placeholder}
								use:autoResize={{ value: customInstructions[opt.key] ?? '', maxHeight: customFieldMaxHeight }}
							></textarea>
						</div>
					{/if}
				{/each}
			</div>

			<h3>Models</h3>
			<div class="row">
				<span>Architect</span>
				<select bind:value={architectModel}>
					{#each appState.models as m (m.id)}
						<option value={m.id}>{m.name}</option>
					{/each}
				</select>
			</div>
			<p class="hint">Judges what changed and elects today's Top Story — Stage A/B's decisions.</p>
			<div class="row">
				<span>Writer</span>
				<select bind:value={writerModel}>
					{#each appState.models as m (m.id)}
						<option value={m.id}>{m.name}</option>
					{/each}
				</select>
			</div>
			<p class="hint">Writes every block's actual content — Stage A/C's prose.</p>

			<h3>Schedule</h3>
			<div class="row">
				<span>Generates at</span>
				<input type="time" bind:value={timeOfDay} />
			</div>
			<p class="hint">Server-local time — no timezone handling.</p>

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
		seed={customInstructions[wizardBlock.key] ?? ''}
		dailyBlockTitle={wizardBlock.label}
		onClose={() => (wizardBlock = null)}
		onAccept={acceptWizardInstruction}
	/>
{/if}

<style>
	h3 {
		margin: var(--space-lg) 0 var(--space-md);
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.12em;
		color: var(--color-text);
	}

	.block-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.block-row {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: 13.5px;
		padding: var(--space-xs) 0;
	}

	.block-subfield {
		margin: 0 0 var(--space-sm) var(--space-xl);
	}

	.field label {
		display: block;
		margin-bottom: var(--space-xs);
		font-size: 12px;
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

	.field textarea {
		width: 100%;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
		font: inherit;
		font-size: 13px;
		color: var(--color-text);
		resize: none;
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		margin-bottom: var(--space-sm);
		font-size: 14px;
	}

	.row select,
	.row input[type='time'] {
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
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
</style>
