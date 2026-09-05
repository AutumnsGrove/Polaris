<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { pulsarDailyState, type PulsarDailyConfigInput } from '$lib/pulsarDaily.svelte';
	import { X } from '@lucide/svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';

	let { onClose }: { onClose: () => void } = $props();

	// blockOptions mirrors gateway/pulsar_daily.go's dailyBlockRegistry —
	// top_story deliberately excluded, same reasoning as the registry's
	// own doc comment: it's Stage B's elevation of whichever watch block
	// wins the ranking pass, not independently toggleable content.
	const blockOptions = [
		{ key: 'word_of_day', label: 'Word of the Day' },
		{ key: 'weather', label: 'Weather' },
		{ key: 'on_this_day', label: 'On This Day' },
		{ key: 'quote', label: 'Quote of the Day' },
		{ key: 'picture_of_day', label: 'Picture of the Day' },
		{ key: 'headlines', label: 'Top Headlines' },
		{ key: 'trending', label: 'Trending Now' },
		{ key: 'tech_science', label: 'Tech & Science' },
		{ key: 'local', label: 'Local' },
		{ key: 'sports', label: 'Sports' }
	];

	const cfg = pulsarDailyState.config;
	let enabledBlocks = $state(new Set(cfg?.enabled_blocks ?? blockOptions.map((b) => b.key)));
	let sportsTeams = $state(cfg?.sports_teams ?? '');
	let architectModel = $state(cfg?.architect_model ?? 'deepseek-pro');
	let writerModel = $state(cfg?.writer_model ?? 'deepseek');
	let timeOfDay = $state(cfg?.time_of_day ?? '07:00');

	let saving = $state(false);
	let error = $state('');

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

		const input: PulsarDailyConfigInput = {
			enabled_blocks: [...enabledBlocks],
			sports_teams: sportsTeams.trim(),
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
						<div class="field sports-field">
							<label for="daily-sports-teams">Which teams/leagues?</label>
							<input
								id="daily-sports-teams"
								type="text"
								bind:value={sportsTeams}
								placeholder="Warriors, 49ers, Premier League"
								required
							/>
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

	.sports-field {
		margin: 0 0 var(--space-sm) var(--space-xl);
	}

	.field label {
		display: block;
		margin-bottom: var(--space-xs);
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.field input {
		width: 100%;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
		font: inherit;
		font-size: 13px;
		color: var(--color-text);
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
