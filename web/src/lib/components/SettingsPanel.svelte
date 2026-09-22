<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import {
		X,
		Moon,
		Sun,
		SunMoon,
		RefreshCw,
		RotateCw,
		Info,
		ChevronLeft,
		Server,
		Container,
		Brain,
		Wrench,
		Cpu,
		Target,
		NotepadText,
		User,
		Mic,
		MapPin
	} from '@lucide/svelte';
	import { FOCUS_MODES } from '$lib/focusModes';
	import type { FocusMode } from '$lib/types';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import MemorySettings from './MemorySettings.svelte';
	import MemoryImport from './MemoryImport.svelte';
	import ToolSettings from './ToolSettings.svelte';
	import ConstellationUsageModal from './ConstellationUsageModal.svelte';

	function close() {
		appState.settings.open = false;
	}

	// Local, not on SettingsState — unlike updateState (see its own doc
	// comment on why that one has to survive an unmount), there's nothing
	// in-flight to preserve here. The panel always reopens on the normal
	// settings view, which is the right default every time.
	let showStats = $state(false);
	let showMemory = $state(false);
	let showMemoryImport = $state(false);
	let showTools = $state(false);
	// Not a sibling-panel-state slot like the others above — Constellation
	// Usage is a wholly separate component/data fetch (see
	// ConstellationUsageModal), just reached via a shortcut link from here.
	let showConstellationUsage = $state(false);

	// "About you" pronoun presets — same segmented-control-plus-custom
	// pattern Constellation's own settings modal used before this moved
	// here (issue tracking the settings-menu unification is separate; see
	// GitHub #83). pronounChoice is one of PRONOUN_PRESETS, 'custom', or ''
	// (nothing picked yet); customPronouns only matters while
	// pronounChoice === 'custom'.
	const PRONOUN_PRESETS = ['he/him', 'she/her', 'they/them'];
	let pronounChoice = $state('');
	let customPronouns = $state('');
	let pronounSynced = false;
	$effect(() => {
		if (appState.settings.loaded && !pronounSynced) {
			pronounSynced = true;
			const saved = appState.settings.personPronouns;
			if (saved === '' || PRONOUN_PRESETS.includes(saved)) {
				pronounChoice = saved;
			} else {
				pronounChoice = 'custom';
				customPronouns = saved;
			}
		}
	});
	function choosePronoun(choice: string) {
		pronounChoice = choice;
		if (choice !== 'custom') void appState.settings.setPersonPronouns(choice);
	}

	// Re-check on every open, not just once at app startup — catches an
	// update that finished (or started, from another tab/device) since
	// the panel was last open, without waiting for a full page reload.
	void appState.settings.checkUpdateStatus(() => appState.busy);
	void appState.settings.loadUsage();

	// toolCallTotal/toolErrorRate collapse the per-tool breakdown from
	// GetStats into the two headline numbers worth a glance here — the
	// full per-tool split is what `polaris stats` is for, not this panel.
	let toolCallTotal = $derived(
		appState.settings.usage
			? Object.values(appState.settings.usage.tool_call_counts).reduce((a, b) => a + b, 0)
			: 0
	);
	let toolErrorTotal = $derived(
		appState.settings.usage
			? Object.values(appState.settings.usage.tool_error_counts).reduce((a, b) => a + b, 0)
			: 0
	);
	let toolErrorRate = $derived(toolCallTotal > 0 ? (toolErrorTotal / toolCallTotal) * 100 : 0);
	let wrapupRate = $derived(
		appState.settings.usage && appState.settings.usage.turn_count > 0
			? (appState.settings.usage.max_turns_wrapup_count / appState.settings.usage.turn_count) * 100
			: 0
	);

	// Includes searxng itself (the baseline, not a fallback) so this row
	// is a full picture of who's actually been answering web_search calls
	// — not just "did a fallback ever fire", which would stay invisible
	// (and look like nothing's being tracked at all) until the first one
	// did.
	const providerLabels: Record<string, string> = {
		searxng: 'SearXNG',
		brave: 'Brave',
		parallel: 'Parallel',
		tavily: 'Tavily'
	};
	let searchProviderCounts = $derived(
		appState.settings.usage
			? Object.entries(appState.settings.usage.search_provider_counts).sort((a, b) => b[1] - a[1])
			: []
	);
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={close} aria-label="Close settings"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Settings">
		<div class="sheet-handle" use:swipeToDismiss={close} aria-hidden="true"></div>

		{#if showStats}
			<div class="modal-panel-header">
				<button class="icon-btn" onclick={() => (showStats = false)} title="Back to settings">
					<ChevronLeft size={18} />
				</button>
				<h2>Usage</h2>
				<button class="icon-btn" onclick={close} title="Close"><X size={18} /></button>
			</div>

			<!-- Same usage-big-cost/usage-section-label/usage-stat-group/
			     usage-stat-row visual language as ConstellationUsageModal —
			     shared in app.css so both panels look the same even though
			     every number here is Polaris's own, not Constellation's. -->
			{#if !appState.settings.usageLoaded}
				<p class="usage-empty">Loading…</p>
			{:else if appState.settings.usageError || !appState.settings.usage}
				<p class="usage-empty">Couldn't load usage stats — check your connection and try again.</p>
			{:else}
				{@const usage = appState.settings.usage}
				<div class="usage-big-cost">
					<div class="amount">${usage.period_cost_usd.toFixed(2)}</div>
					<div class="caption">last 30 days &middot; ${usage.total_cost_usd.toFixed(2)} all-time</div>
				</div>

				<div class="usage-section-label">Cost by source <span class="usage-section-sublabel">(30d / all-time)</span></div>
				<div class="usage-stat-group">
					<div class="usage-stat-row">
						<span class="label">Polaris</span>
						<span class="value"
							>${usage.cost_by_source.polaris.period_cost_usd.toFixed(2)} / ${usage.cost_by_source.polaris.total_cost_usd.toFixed(
								2
							)}</span
						>
					</div>
					<div class="usage-stat-row sub">
						<span class="label">Verification</span>
						<span class="value"
							>${usage.verification_cost_usd.period_cost_usd.toFixed(4)} / ${usage.verification_cost_usd.total_cost_usd.toFixed(
								4
							)}</span
						>
					</div>
					<div class="usage-stat-row">
						<span class="label">Pulsar</span>
						<span class="value"
							>${usage.cost_by_source.pulsar.period_cost_usd.toFixed(2)} / ${usage.cost_by_source.pulsar.total_cost_usd.toFixed(
								2
							)}</span
						>
					</div>
					<div class="usage-stat-row">
						<span class="label">Daily</span>
						<span class="value"
							>${usage.cost_by_source.daily.period_cost_usd.toFixed(2)} / ${usage.cost_by_source.daily.total_cost_usd.toFixed(
								2
							)}</span
						>
					</div>
				</div>

				<div class="usage-section-label">Activity</div>
				<div class="usage-stat-group">
					<div class="usage-stat-row">
						<span class="label">Threads / turns</span>
						<span class="value">{usage.thread_count} / {usage.turn_count}</span>
					</div>
					<div class="usage-stat-row">
						<span class="label">Tool calls</span>
						<span class="value">{toolCallTotal} ({toolErrorRate.toFixed(1)}% errored)</span>
					</div>
					{#if usage.transponder_call_count > 0}
						<div class="usage-stat-row">
							<span class="label">Transponder calls</span>
							<span class="value">{usage.transponder_call_count}</span>
						</div>
					{/if}
					{#if searchProviderCounts.length > 0}
						<div class="usage-stat-row">
							<span class="label">web_search providers</span>
							<span class="value"
								>{searchProviderCounts
									.map(([provider, count]) => `${providerLabels[provider] ?? provider}: ${count}`)
									.join(', ')}</span
							>
						</div>
					{/if}
					{#if appState.settings.usage && appState.settings.usage.code_exec_wall_time_ms > 0}
						<div class="usage-stat-row">
							<span class="label">code_exec wall time</span>
							<span class="value">{(appState.settings.usage.code_exec_wall_time_ms / 1000).toFixed(1)}s</span>
						</div>
					{/if}
				</div>

				<div class="usage-section-label">Health</div>
				<div class="usage-stat-group">
					<div class="usage-stat-row warn">
						<span class="label">Ran out of turn budget</span>
						<span class="value">{usage.max_turns_wrapup_count} ({wrapupRate.toFixed(1)}% of turns)</span>
					</div>
					<div class="usage-stat-row">
						<span class="label">Check-in nudges</span>
						<span class="value">{usage.check_in_count}</span>
					</div>
					<div class="usage-stat-row">
						<span class="label">Stale-streak warnings</span>
						<span class="value">{usage.stale_streak_count}</span>
					</div>
					<div class="usage-stat-row">
						<span class="label">Auto-compactions</span>
						<span class="value">{usage.compaction_count}</span>
					</div>
				</div>

				<p class="hint">Run <code>polaris stats</code> for the full per-tool breakdown.</p>
				<!-- Constellation's own spend is deliberately not folded into
				     the totals above — a separate surface, own data fetch, own
				     panel (see docs/plans/constellation.md's "Cost tracking and
				     observability"). This is just a nav shortcut into it. -->
				<button class="constellation-usage-link" onclick={() => (showConstellationUsage = true)}>
					&rarr; Constellation usage
				</button>
			{/if}
		{:else if showMemoryImport}
			<div class="modal-panel-header">
				<button
					class="icon-btn"
					onclick={() => (showMemoryImport = false)}
					title="Back to Memory"
				>
					<ChevronLeft size={18} />
				</button>
				<h2>Import memories</h2>
				<button class="icon-btn" onclick={close} title="Close"><X size={18} /></button>
			</div>

			<MemoryImport />
		{:else if showMemory}
			<div class="modal-panel-header">
				<button class="icon-btn" onclick={() => (showMemory = false)} title="Back to settings">
					<ChevronLeft size={18} />
				</button>
				<h2>Memory</h2>
				<button class="icon-btn" onclick={close} title="Close"><X size={18} /></button>
			</div>

			<MemorySettings onImport={() => (showMemoryImport = true)} />
		{:else if showTools}
			<div class="modal-panel-header">
				<button class="icon-btn" onclick={() => (showTools = false)} title="Back to settings">
					<ChevronLeft size={18} />
				</button>
				<h2>Tools</h2>
				<button class="icon-btn" onclick={close} title="Close"><X size={18} /></button>
			</div>

			<ToolSettings />
		{:else}
			<div class="modal-panel-header">
				<h2>Settings</h2>
				<div class="header-actions">
					<button class="icon-btn" onclick={() => (showStats = true)} title="Usage stats">
						<Info size={18} />
					</button>
					<button class="icon-btn" onclick={close} title="Close"><X size={18} /></button>
				</div>
			</div>

			<div class="section-head"><SunMoon size={15} /><span class="section-title">Appearance</span></div>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label">Theme</span>
					<div class="theme-toggle">
						<button
							class:active={appState.settings.theme === 'dark'}
							onclick={() => appState.settings.setTheme('dark')}
						>
							<Moon size={14} /> Dark
						</button>
						<button
							class:active={appState.settings.theme === 'light'}
							onclick={() => appState.settings.setTheme('light')}
						>
							<Sun size={14} /> Light
						</button>
					</div>
				</div>
			</div>

			<div class="section-head"><Cpu size={15} /><span class="section-title">Model</span></div>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label">Default model</span>
					<select
						value={appState.settings.defaultModel}
						onchange={(e) => appState.settings.setDefaultModel(e.currentTarget.value, () => appState.loadModels())}
					>
						{#each appState.models as model (model.id)}
							<option value={model.id}>{model.name}</option>
						{/each}
					</select>
				</div>
			</div>
			<p class="hint">
				Applies to new threads. You can still switch models per-thread from the chat header.
			</p>

			<div class="section-head"><Target size={15} /><span class="section-title">Focus</span></div>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label">Default focus mode</span>
					<select
						value={appState.settings.defaultFocusMode}
						onchange={(e) => appState.settings.setDefaultFocusMode(e.currentTarget.value as FocusMode)}
					>
						<option value="off">Off</option>
						{#each FOCUS_MODES as mode (mode.id)}
							<option value={mode.id}>{mode.label}</option>
						{/each}
					</select>
				</div>
			</div>
			<p class="hint">
				Applied to every new message until changed from the composer's "+" menu.
			</p>

			<div class="section-head"><NotepadText size={15} /><span class="section-title">Custom instructions</span></div>
			<div class="settings-group">
				<div class="settings-row stacked">
					<textarea
						class="custom-instructions-input"
						placeholder="e.g. Always answer in French. I'm a nurse — use clinical terminology."
						maxlength="4000"
						value={appState.settings.customInstructions}
						onblur={(e) => appState.settings.setCustomInstructions(e.currentTarget.value)}
					></textarea>
				</div>
			</div>
			<p class="hint">
				Added to every answer as steering, on top of <code>prompt.md</code>. Edit
				<code>prompt.md</code> directly for anything more involved than a short standing
				preference.
			</p>

			<div class="section-head"><User size={15} /><span class="section-title">About you</span></div>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label">Name</span>
					<input
						type="text"
						class="inline-input"
						placeholder="e.g. Alex"
						maxlength="80"
						value={appState.settings.personName}
						onblur={(e) => appState.settings.setPersonName(e.currentTarget.value)}
					/>
				</div>
				<div class="settings-row stacked">
					<span class="row-label">Pronouns</span>
					<div class="theme-toggle person-pronoun-toggle">
						{#each PRONOUN_PRESETS as preset (preset)}
							<button
								type="button"
								class:active={pronounChoice === preset}
								onclick={() => choosePronoun(preset)}
							>
								{preset}
							</button>
						{/each}
						<button
							type="button"
							class:active={pronounChoice === 'custom'}
							onclick={() => choosePronoun('custom')}
						>
							Custom
						</button>
					</div>
					{#if pronounChoice === 'custom'}
						<input
							type="text"
							class="stacked-input"
							placeholder="e.g. ze/zir"
							maxlength="40"
							value={customPronouns}
							onblur={(e) => appState.settings.setPersonPronouns(e.currentTarget.value)}
						/>
					{/if}
				</div>
			</div>
			<p class="hint">
				Used by both this assistant and Constellation's Weaver — without it, either has to guess
				pronouns from context (and can guess wrong).
			</p>

			<div class="section-head"><Mic size={15} /><span class="section-title">Voice</span></div>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label">Mic button</span>
					<div class="theme-toggle">
						<button
							class:active={appState.settings.voiceInputMode === 'toggle'}
							onclick={() => appState.settings.setVoiceInputMode('toggle')}
						>
							Tap to toggle
						</button>
						<button
							class:active={appState.settings.voiceInputMode === 'hold'}
							onclick={() => appState.settings.setVoiceInputMode('hold')}
						>
							Hold to talk
						</button>
					</div>
				</div>
			</div>
			<p class="hint">
				"Tap to toggle" starts recording on the first tap and stops on the second — no need to
				keep a finger down for the whole memo. "Hold to talk" is the original press-and-hold
				behavior.
			</p>

			<div class="section-head"><MapPin size={15} /><span class="section-title">Location</span></div>
			<div class="settings-group">
				<div class="settings-row stacked">
					<input
						type="text"
						class="stacked-input"
						placeholder="e.g. Seattle, WA"
						value={appState.settings.manualLocation}
						onchange={(e) => appState.settings.setManualLocation(e.currentTarget.value)}
					/>
				</div>
			</div>
			<p class="hint">
				Used by "near me" questions when the browser can't get your real location (it needs
				https://, not this app's plain Tailscale IP). Ignored automatically once a real GPS fix
				is available.
			</p>

			<div class="section-head"><Brain size={15} /><span class="section-title">Memory</span></div>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label">Enabled</span>
					<label class="switch">
						<input
							type="checkbox"
							checked={appState.settings.memoryEnabled}
							onchange={(e) => appState.settings.setMemoryEnabled(e.currentTarget.checked)}
						/>
						<span class="slider"></span>
					</label>
				</div>
			</div>
			<div class:section-disabled={!appState.settings.memoryEnabled}>
				<div class="settings-group">
					<div class="settings-row">
						<span class="row-label">What <span class="wordmark">Polaris</span> remembers about you</span>
						<button
							class="btn manage-btn"
							onclick={() => appState.settings.memoryEnabled && (showMemory = true)}
							disabled={!appState.settings.memoryEnabled}
						>
							<Brain size={14} /> Manage
						</button>
					</div>
				</div>
				<p class="hint">
					View, edit by telling <span class="wordmark">Polaris</span> what to change, or forget things
					it's saved across conversations.
				</p>
			</div>

			<div class="section-head"><Wrench size={15} /><span class="section-title">Tools</span></div>
			<div class="settings-group">
				<div class="settings-row">
					<span class="row-label">Which tools <span class="wordmark">Polaris</span> can use</span>
					<button class="btn manage-btn" onclick={() => (showTools = true)}>
						<Wrench size={14} /> Manage
					</button>
				</div>
			</div>
			<p class="hint">
				Turn off individual tools, or use the composer's "+" menu to turn off research entirely
				for a plain chat.
			</p>

			<div class="section-head"><RefreshCw size={15} /><span class="section-title">Updates</span></div>
			{#if appState.version}
				<div class="settings-group">
					<div class="settings-row">
						<span class="row-label">Version</span>
						<span class="version-info">
							<code class="version">{appState.version}</code>
							{#if appState.deployment === 'docker'}
								<span class="deployment-icon" title="Running in Docker">
									<Container size={13} />
								</span>
							{:else if appState.deployment === 'bare-metal'}
								<span class="deployment-icon" title="Running bare-metal">
									<Server size={13} />
								</span>
							{/if}
						</span>
					</div>
				</div>
			{/if}
			<div class="update-actions">
				<button
					class="btn update-btn"
					onclick={() => appState.settings.pushUpdate(() => appState.busy)}
					disabled={appState.settings.updateState !== 'idle' && appState.settings.updateState !== 'error'}
				>
					<RefreshCw
						size={14}
						class={appState.settings.updateKind === 'update' &&
						(appState.settings.updateState === 'updating' || appState.settings.updateState === 'restarting')
							? 'spin'
							: ''}
					/>
					{#if appState.settings.updateKind === 'update' && appState.settings.updateState === 'updating'}
						Pulling & building…
					{:else if appState.settings.updateKind === 'update' && appState.settings.updateState === 'restarting'}
						Restarting…
					{:else}
						Update <span class="wordmark">Polaris</span>
					{/if}
				</button>
				<!-- No pull, no rebuild — just kills and cleanly restarts the
				     running binary. Separate from Update Polaris because running
				     the full update flow just to force a restart still does a
				     real (if usually no-op) git pull and go build first, which
				     can stall for no benefit when there's nothing new to pull. -->
				<button
					class="btn restart-btn"
					onclick={() => appState.settings.pushRestart(() => appState.busy)}
					disabled={appState.settings.updateState !== 'idle' && appState.settings.updateState !== 'error'}
				>
					<RotateCw
						size={14}
						class={appState.settings.updateKind === 'restart' &&
						(appState.settings.updateState === 'updating' || appState.settings.updateState === 'restarting')
							? 'spin'
							: ''}
					/>
					{#if appState.settings.updateKind === 'restart' && (appState.settings.updateState === 'updating' || appState.settings.updateState === 'restarting')}
						Restarting…
					{:else}
						Restart <span class="wordmark">Polaris</span>
					{/if}
				</button>
			</div>
			<p class="hint">
				<strong>Update</strong> pulls the latest code, rebuilds, then restarts.
				<strong>Restart</strong> just cleanly restarts the running process — no pull, no rebuild.
			</p>
			{#if appState.settings.updateLog}
				<pre class="log">{appState.settings.updateLog}</pre>
			{/if}
		{/if}
	</div>
</div>

{#if showConstellationUsage}
	<ConstellationUsageModal onClose={() => (showConstellationUsage = false)} />
{/if}

<style>
	/* .modal-backdrop/.modal-panel/.modal-panel-header live in app.css —
	   shared with ComposerMenu.svelte, one popup treatment (including the
	   mobile bottom-sheet behavior) for the whole app instead of two
	   copies to keep in sync by hand. Same for .usage-big-cost/
	   .usage-section-label/.usage-stat-group/.usage-stat-row, shared with
	   ConstellationUsageModal.svelte's own Usage section. */

	.header-actions {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
	}

	/* Card-grouped row pattern, matching ConstellationSettingsModal.svelte
	   exactly (issue #83 — bringing every settings surface onto one visual
	   language instead of N divergent ones). Duplicated here rather than
	   shared, same reasoning as .switch below: Svelte scopes component
	   styles per-file, so this is copy-once-per-component by design, not
	   an oversight.

	   .section-label used to be the header treatment (11px uppercase,
	   --color-text-dim) — the same dim color .hint uses below, so a
	   header and its own caption text read as one undifferentiated gray
	   block. .section-head/.section-title (a gold icon + full-text-color
	   title + a full-width rule) replaces it; see
	   mockups/settings-header-hierarchy.html option D for the full set of
	   alternatives this was picked from. */
	.section-head {
		display: flex;
		align-items: center;
		gap: 9px;
		margin-top: var(--space-2xl);
		margin-bottom: var(--space-xs);
	}

	/* The very first header sits right under the panel's own title bar —
	   no previous section to separate itself from, so it skips the
	   between-sections gap every other header gets. */
	.modal-panel-header + .section-head {
		margin-top: 0;
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
		/* Tight on purpose — the gap that actually separates one section
		   from the next now lives on .section-head's margin-top, so this
		   only needs to hug the .hint paragraph directly below it. */
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

	/* Used where a row's control doesn't fit beside its label on one line
	   (a full segmented control, a full-width text field) — label on its
	   own line, content below it, instead of forcing a cramped inline fit. */
	.settings-row.stacked {
		flex-direction: column;
		align-items: stretch;
		gap: var(--space-sm);
	}

	.row-label {
		font-size: 14px;
		font-weight: 500;
	}

	.settings-row select {
		font: inherit;
		font-size: 13px;
		background: var(--color-surface-3);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
		padding: var(--space-xs) var(--space-sm);
	}

	/* Compact, inline field beside its own row-label (Name) — sized like
	   the select above it, not stretched full-width the way a stacked
	   field is. */
	.inline-input {
		font: inherit;
		font-size: 13px;
		background: var(--color-surface-3);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
		padding: var(--space-xs) var(--space-sm);
		width: 140px;
	}

	.usage-empty {
		text-align: center;
		font-size: 13.5px;
		color: var(--color-text-dim);
		padding: var(--space-2xl) 0;
	}

	/* Dims the rest of a section (everything below its own on/off row)
	   when that section's feature is turned off — e.g. Memory's "What
	   Polaris remembers"/hint once Enabled is switched off. pointer-events
	   is the real block for anything without its own disabled attribute;
	   the Manage button also gets disabled directly (see the markup above)
	   for proper keyboard/screen-reader behavior, not just a dimmed look. */
	.section-disabled {
		opacity: 0.45;
		pointer-events: none;
	}

	/* Same switch construction as ComposerMenu.svelte/ToolSettings.svelte —
	   duplicated rather than shared since Svelte scopes component styles
	   per-file, but it's the same visual vocabulary everywhere a boolean
	   setting appears. */
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

	.hint {
		font-size: 12px;
		color: var(--color-text-dim);
		margin: var(--space-sm) 0 0 0;
	}

	.hint code {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 11px;
	}

	.constellation-usage-link {
		display: block;
		margin-top: var(--space-md);
		background: none;
		border: none;
		padding: 0;
		font: inherit;
		font-size: 12.5px;
		color: var(--color-accent);
		cursor: pointer;
	}

	/* Real segmented-control construction, not a bordered box of buttons:
	   a recessed track (the well shadow reused from inputs/readouts) with
	   a floating pill for whichever option is active — this is how
	   Apple's own UISegmentedControl is actually built, track + thumb,
	   not "button, button, divider, button". The thumb's radius is a
	   couple px tighter than the track's so it visibly nests inside it
	   rather than sharing one uniform radius throughout. */
	.theme-toggle {
		display: flex;
		gap: var(--space-xs);
		background: var(--color-bg);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-xs);
	}

	.theme-toggle button {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		border: none;
		background: transparent;
		border-radius: calc(var(--radius-md) - 3px);
		padding: var(--space-sm) var(--space-md);
		font-size: 13px;
		color: var(--color-text-dim);
		transition: background-color 0.15s var(--ease-out-expo), color 0.15s var(--ease-out-expo), box-shadow 0.15s var(--ease-out-expo);
	}

	.theme-toggle button:hover {
		color: var(--color-text);
	}

	.theme-toggle button.active {
		background: var(--color-surface-3);
		color: var(--color-text);
		font-weight: 600;
		box-shadow: var(--shadow-xs);
	}

	.person-pronoun-toggle {
		flex-wrap: wrap;
	}

	/* Full-width, "carved into the surface" field — used wherever a
	   .settings-row.stacked's control is a free-text field rather than a
	   toggle/segmented-control (custom pronouns, manual location). */
	.stacked-input {
		width: 100%;
		border: none;
		background: var(--color-surface-3);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
		font-size: 13px;
		color: var(--color-text);
	}

	.stacked-input::placeholder {
		color: var(--color-text-dim);
	}

	.custom-instructions-input {
		width: 100%;
		min-height: 72px;
		resize: vertical;
		border: none;
		background: var(--color-surface-3);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
		font-size: 13px;
		font-family: inherit;
		color: var(--color-text);
	}

	.custom-instructions-input::placeholder {
		color: var(--color-text-dim);
	}

	.version {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 12px;
		color: var(--color-text-dim);
		background: var(--color-bg);
		padding: var(--space-xs) var(--space-sm);
		border-radius: var(--radius-sm);
		border: none;
		box-shadow: var(--shadow-well);
	}

	.version-info {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	/* Matches PRODUCT.md's "calm over clever" — a plain muted glyph, not a
	   colored badge; a hover title is enough to name it explicitly. */
	.deployment-icon {
		display: inline-flex;
		color: var(--color-text-dim);
	}

	.update-actions {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.update-btn,
	.restart-btn {
		width: 100%;
	}

	.log {
		margin-top: var(--space-md);
		padding: var(--space-md) var(--space-md);
		background: var(--color-bg);
		border: none;
		box-shadow: var(--shadow-well);
		border-radius: var(--radius-sm);
		font-size: 11px;
		line-height: 1.5;
		color: var(--color-text-dim);
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 180px;
		overflow-y: auto;
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
	}

	/* Same reserved-brand-face treatment used everywhere else "Polaris"
	   appears as a name (ChatView's welcome heading, the sidebar wordmark,
	   ModeToggle's switcher) — never left in the surrounding sans-serif. */
	.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		font-size: 1.05em;
		letter-spacing: 0.02em;
	}

	:global(.spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
