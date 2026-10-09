<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import ToolUsageBars from './ToolUsageBars.svelte';
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
		IdCard,
		UsersRound,
		Volume2,
		BookOpenText,
		Ghost,
		Tag,
		Mic,
		MapPin,
		Galaxy,
		Gem,
		Venus,
		Mars,
		Play,
		Square
	} from '@lucide/svelte';
	import { VISUALS_MODES, type VisualsMode } from '$lib/settings.svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import { FOCUS_MODES } from '$lib/focusModes';
	import type { FocusMode } from '$lib/types';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import MemorySettings from './MemorySettings.svelte';
	import { globalMemorySource } from '$lib/memorySource';
	import MemoryImport from './MemoryImport.svelte';
	import ToolSettings from './ToolSettings.svelte';
	import Switch from './Switch.svelte';
	import SettingsRow from './SettingsRow.svelte';
	import Asterism from './Asterism.svelte';
	import ConstellationUsageModal from './ConstellationUsageModal.svelte';
	import PulsarUsageModal from './PulsarUsageModal.svelte';
	import WizardButton from './WizardButton.svelte';
	import WizardOverlay from './WizardOverlay.svelte';

	// Voice previews are pre-rendered static clips (dev/gen-voice-samples.sh),
	// so they play instantly and cost nothing. One shared element: starting
	// another voice, or tapping the playing one again, stops the current clip.
	let playingVoice = $state<string | null>(null);
	let previewAudio: HTMLAudioElement | null = null;

	function stopPreview() {
		previewAudio?.pause();
		previewAudio = null;
		playingVoice = null;
	}

	function togglePreview(id: string) {
		const wasPlaying = playingVoice === id;
		stopPreview();
		if (wasPlaying) return;
		const audio = new Audio(`/voice-samples/${id}.mp3`);
		previewAudio = audio;
		playingVoice = id;
		audio.addEventListener('ended', () => {
			if (previewAudio === audio) stopPreview();
		});
		audio.play().catch(() => {
			if (previewAudio === audio) stopPreview();
		});
	}

	function close() {
		stopPreview();
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
	let showPulsarUsage = $state(false);
	// "Help me write this" for the global custom instructions box below —
	// see WizardOverlay / gateway/wizard.go's global_instructions target.
	// Unlike a Field's instructions tab (which has an explicit Save), this
	// box already saves itself (on blur), so accepting a draft goes straight
	// through setCustomInstructions rather than only filling the textarea.
	let showInstructionsWizard = $state(false);

	// One-line summary shown under the Prism control for each setting. (Prism
	// is the user-facing name; the setting key, state field and Go identifiers
	// keep the older plain "visuals" so a stored value isn't orphaned.)
	const VISUALS_DESC: Record<VisualsMode, string> = {
		off: 'Plain text answers only',
		low: 'A block only when it clearly beats prose',
		normal: 'Comparisons and steps when they fit'
	};

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
	// The Constellation toggle below reads the shared config; usually the
	// sidebar already loaded it, this covers a cold open.
	if (!constellationState.config) void constellationState.loadConfig();

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

	// round2 + the two headline totals below exist so the big "$X.XX" at
	// the top of this panel always equals the sum of the four rows
	// literally printed underneath it in "Cost by source" — a real,
	// reported point of confusion otherwise: usage.total_cost_usd/
	// period_cost_usd are exact floats (store.Stats.TotalCostUSD is the
	// true sum of all four unrounded buckets), but four independently-
	// rounded-to-cents rows can round to a sum a cent off from that exact
	// total rounded on its own (e.g. 0.994 total displays as $0.99, but
	// 0.337+0.331+0.326 displays as $0.34+$0.33+$0.33 = $1.00) — visually
	// "doesn't add up" even though every individual figure is correct.
	// Rounding each row first and summing those makes the headline
	// consistent with what's actually on screen, by construction.
	function round2(n: number): number {
		return Math.round(n * 100) / 100;
	}
	let headlinePeriodCostUSD = $derived(
		appState.settings.usage
			? round2(appState.settings.usage.cost_by_source.polaris.period_cost_usd) +
					round2(appState.settings.usage.cost_by_source.pulsar.period_cost_usd) +
					round2(appState.settings.usage.cost_by_source.daily.period_cost_usd) +
					round2(appState.settings.usage.cost_by_source.constellation.period_cost_usd)
			: 0
	);
	let headlineTotalCostUSD = $derived(
		appState.settings.usage
			? round2(appState.settings.usage.cost_by_source.polaris.total_cost_usd) +
					round2(appState.settings.usage.cost_by_source.pulsar.total_cost_usd) +
					round2(appState.settings.usage.cost_by_source.daily.total_cost_usd) +
					round2(appState.settings.usage.cost_by_source.constellation.total_cost_usd)
			: 0
	);

	// Prompt-cache hit rate — the deployment-wide version of ThreadMenu's
	// per-thread "Cache hits" row. "—" when no turn has recorded usage in
	// that window, rather than a misleading 0%.
	function cacheHitPercent(prompt: number | undefined, cached: number | undefined): string {
		return prompt ? `${Math.round(((cached ?? 0) / prompt) * 100)}%` : '—';
	}
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
					<div class="amount">${headlinePeriodCostUSD.toFixed(2)}</div>
					<div class="caption">last 30 days &middot; ${headlineTotalCostUSD.toFixed(2)} all-time</div>
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
					{#if usage.oracle_cost_usd.total_cost_usd > 0}
						<div class="usage-stat-row sub">
							<span class="label">Oracle</span>
							<span class="value"
								>${usage.oracle_cost_usd.period_cost_usd.toFixed(4)} / ${usage.oracle_cost_usd.total_cost_usd.toFixed(
									4
								)}</span
							>
						</div>
					{/if}
					<!-- Call counts sit with the Jev cost lines they explain: a few
					     hundredths of a cent each, so the dollar figure alone can't say
					     whether it was 30 calls or 3,000. These count API calls, not
					     questions asked. -->
					{#if usage.jev_calls}
						{#each [{ label: 'Badge calls', c: usage.jev_calls.verification }, { label: 'compare_sources calls', c: usage.jev_calls.compare }, { label: 'Oracle calls', c: usage.jev_calls.oracle }] as row (row.label)}
							{#if row.c.total > 0}
								<div class="usage-stat-row sub">
									<span class="label">{row.label}</span>
									<span class="value">{row.c.period} / {row.c.total}</span>
								</div>
							{/if}
						{/each}
					{/if}
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
					<div class="usage-stat-row">
						<span class="label">Constellation</span>
						<span class="value"
							>${usage.cost_by_source.constellation.period_cost_usd.toFixed(
								2
							)} / ${usage.cost_by_source.constellation.total_cost_usd.toFixed(2)}</span
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

				<div class="usage-section-label">Tools <span class="usage-section-sublabel">(30d)</span></div>
				<ToolUsageBars
					calls={usage.tool_call_counts}
					errors={usage.tool_error_counts}
					madeUp={usage.made_up_tool_counts}
				/>

				<div class="usage-section-label">Health</div>
				<div class="usage-stat-group">
					{#if usage.cache_usage}
						<div class="usage-stat-row">
							<span class="label">Prompt cache hits</span>
							<span class="value"
								>{cacheHitPercent(usage.cache_usage.period_prompt_tokens, usage.cache_usage.period_cache_read_tokens)}
								({cacheHitPercent(usage.cache_usage.total_prompt_tokens, usage.cache_usage.total_cache_read_tokens)} all-time)</span
							>
						</div>
					{/if}
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
				<!-- Constellation's cost is now counted in the totals above
				     (the "Constellation" row in Cost by source) — this shortcut
				     is just for the richer star/tool/review breakdown
				     ConstellationUsageModal has room for and this panel doesn't. -->
				<button class="constellation-usage-link" onclick={() => (showConstellationUsage = true)}>
					&rarr; Constellation usage
				</button>
				<!-- Same reasoning as the Constellation shortcut just above —
				     Pulsar's cost is already in the "Pulsar" row above; this is
				     just a shortcut to PulsarUsageModal's own tool-call/
				     failure-rate/nudge breakdown, which this panel has no room
				     for. -->
				<button class="constellation-usage-link" onclick={() => (showPulsarUsage = true)}>
					&rarr; Pulsar usage
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

			<MemoryImport source={globalMemorySource(appState.settings)} />
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

			<div class="settings-label usage-section-label">General</div>
			<div class="settings-group">
				<SettingsRow icon={SunMoon} title="Theme">
					{#snippet control()}
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
					{/snippet}
				</SettingsRow>
				<SettingsRow icon={Cpu} title="Default model" desc="Used for new threads">
					{#snippet control()}
						<select
							aria-label="Default model"
							value={appState.settings.defaultModel}
							onchange={(e) => appState.settings.setDefaultModel(e.currentTarget.value, () => appState.loadModels())}
						>
							{#each appState.models as model (model.id)}
								<option value={model.id}>{model.name}</option>
							{/each}
						</select>
					{/snippet}
				</SettingsRow>
				<SettingsRow icon={Target} title="Default focus" desc="Applied to new messages">
					{#snippet control()}
						<select
							aria-label="Default focus mode"
							value={appState.settings.defaultFocusMode}
							onchange={(e) => appState.settings.setDefaultFocusMode(e.currentTarget.value as FocusMode)}
						>
							<option value="off">Off</option>
							{#each FOCUS_MODES as mode (mode.id)}
								<option value={mode.id}>{mode.label}</option>
							{/each}
						</select>
					{/snippet}
				</SettingsRow>
			</div>

			<div class="settings-label usage-section-label">You</div>
			<div class="settings-group">
				<SettingsRow icon={NotepadText} title="Custom instructions" desc="Steers every answer">
					{#snippet children()}
						<div class="wizard-row">
							<WizardButton onclick={() => (showInstructionsWizard = true)} />
						</div>
						<textarea
							class="custom-instructions-input"
							aria-label="Custom instructions"
							placeholder="e.g. Always answer in French. I'm a nurse — use clinical terminology."
							maxlength="4000"
							value={appState.settings.customInstructions}
							onblur={(e) => appState.settings.setCustomInstructions(e.currentTarget.value)}
						></textarea>
					{/snippet}
					{#snippet details()}
						Added to every answer on top of <code>prompt.md</code>. Edit <code>prompt.md</code> directly
						for anything more involved than a short standing preference.
					{/snippet}
				</SettingsRow>
				<SettingsRow icon={IdCard} title="Name">
					{#snippet control()}
						<input
							type="text"
							class="inline-input"
							aria-label="Name"
							placeholder="e.g. Alex"
							maxlength="80"
							value={appState.settings.personName}
							onblur={(e) => appState.settings.setPersonName(e.currentTarget.value)}
						/>
					{/snippet}
				</SettingsRow>
				<SettingsRow icon={UsersRound} title="Pronouns" desc="Used by Polaris and Constellation's Weaver">
					{#snippet children()}
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
								aria-label="Custom pronouns"
								placeholder="e.g. ze/zir"
								maxlength="40"
								value={customPronouns}
								onblur={(e) => appState.settings.setPersonPronouns(e.currentTarget.value)}
							/>
						{/if}
					{/snippet}
				</SettingsRow>
				<SettingsRow icon={MapPin} title="Location" desc="For “near me” questions">
					{#snippet control()}
						<input
							type="text"
							class="inline-input"
							aria-label="Location"
							placeholder="e.g. Seattle, WA"
							value={appState.settings.manualLocation}
							onchange={(e) => appState.settings.setManualLocation(e.currentTarget.value)}
						/>
					{/snippet}
					{#snippet details()}
						Used when the browser can't get your real location (it needs https://, not this app's plain
						Tailscale IP). Ignored automatically once a real GPS fix is available.
					{/snippet}
				</SettingsRow>
			</div>

			<div class="settings-label usage-section-label">Voice</div>
			<div class="settings-group">
				<SettingsRow
					icon={Mic}
					title="Mic button"
					desc={appState.settings.voiceInputMode === 'toggle'
						? 'Tap to start and stop'
						: 'Hold while speaking'}
				>
					{#snippet control()}
						<div class="theme-toggle">
							<button
								class:active={appState.settings.voiceInputMode === 'toggle'}
								onclick={() => appState.settings.setVoiceInputMode('toggle')}
							>
								Tap
							</button>
							<button
								class:active={appState.settings.voiceInputMode === 'hold'}
								onclick={() => appState.settings.setVoiceInputMode('hold')}
							>
								Hold
							</button>
						</div>
					{/snippet}
				</SettingsRow>
				{#if appState.settings.ttsVoices.length > 0}
					<SettingsRow
						icon={Volume2}
						title="Reading voice"
						desc="Read-aloud and calls · tap ▶ to preview"
					>
						{#snippet children()}
							<div class="voice-grid" role="radiogroup" aria-label="Reading voice">
								{#each appState.settings.ttsVoices as v (v.id)}
									<div class="voice-option" class:active={appState.settings.ttsVoice === v.id}>
										<button
											class="voice-select"
											role="radio"
											aria-checked={appState.settings.ttsVoice === v.id}
											onclick={() => appState.settings.setTTSVoice(v.id)}
										>
											<span class="voice-flag" aria-hidden="true">{v.accent === 'british' ? '🇬🇧' : '🇺🇸'}</span>
											<span class="voice-name">{v.name}</span>
											<span
												class="voice-gender"
												role="img"
												aria-label={`${v.accent === 'british' ? 'British' : 'American'} ${v.gender}`}
											>
												{#if v.gender === 'female'}<Venus size={14} />{:else}<Mars size={14} />{/if}
											</span>
										</button>
										<button
											class="voice-preview"
											aria-label={playingVoice === v.id ? `Stop ${v.name} preview` : `Preview ${v.name}`}
											onclick={() => togglePreview(v.id)}
										>
											{#if playingVoice === v.id}<Square size={12} />{:else}<Play size={12} />{/if}
										</button>
									</div>
								{/each}
							</div>
						{/snippet}
					</SettingsRow>
				{/if}
			</div>

			<div class="settings-label usage-section-label">Features</div>
			<div class="settings-group">
				<SettingsRow icon={Brain} title="Memory" desc="Remember things across conversations">
					{#snippet control()}
						<Switch
							label="Memory enabled"
							checked={appState.settings.memoryEnabled}
							onchange={(v) => appState.settings.setMemoryEnabled(v)}
						/>
					{/snippet}
				</SettingsRow>
				<SettingsRow
					icon={BookOpenText}
					title="What Polaris remembers"
					desc="View, edit, or forget"
					disabled={!appState.settings.memoryEnabled}
					onclick={() => (showMemory = true)}
				/>
				<SettingsRow icon={Asterism} title="Oracle mode" desc="Quietly adjusts answers per question">
					{#snippet control()}
						<Switch
							label="Oracle mode enabled"
							checked={appState.settings.oracleEnabled}
							onchange={(v) => appState.settings.setOracleEnabled(v)}
						/>
					{/snippet}
					{#snippet details()}
						Reads each message before answering and quietly adjusts how Polaris answers — for example,
						taking extra care with sources on a health question. Anything it changes is shown on the reply,
						and you can undo it with a tap. Off by default. Each message is also sent to a small helper
						model, and its cost appears under Usage. Ghost conversations stay out of this unless you turn
						on the second switch; even then, Oracle won't offer to set up a Pulsar, add to Daily, or file
						the chat into a Field from a conversation that's meant to leave no trace.
					{/snippet}
				</SettingsRow>
				<SettingsRow icon={Ghost} title="Also in ghost conversations">
					{#snippet control()}
						<Switch
							label="Oracle mode in ghost conversations"
							checked={appState.settings.oracleGhostEnabled}
							onchange={(v) => appState.settings.setOracleGhostEnabled(v)}
						/>
					{/snippet}
				</SettingsRow>
				<SettingsRow icon={Gem} title="Prism" desc={VISUALS_DESC[appState.settings.visuals]}>
					{#snippet control()}
						<div class="theme-toggle" role="group" aria-label="Prism">
							{#each VISUALS_MODES as mode (mode)}
								<button
									class:active={appState.settings.visuals === mode}
									aria-pressed={appState.settings.visuals === mode}
									onclick={() => appState.settings.setVisuals(mode)}
								>
									{mode === 'off' ? 'Off' : mode === 'low' ? 'Low' : 'Normal'}
								</button>
							{/each}
						</div>
					{/snippet}
					{#snippet details()}
						Lets Polaris answer with a comparison, a step-by-step rail, a callout, or a big number when
						that is clearly easier to scan than prose. Low is the quiet default: a block only when it
						clearly beats text. Normal reaches for one more readily. Off keeps every answer plain. Applies
						to chat and Pulsar; Daily and Atlas stay as they are. Blocks already in a conversation keep
						showing whatever this is set to.
					{/snippet}
				</SettingsRow>
				<SettingsRow icon={Galaxy} title="Constellation" desc="Weaver builds your library of stars">
					{#snippet control()}
						<Switch
							label="Constellation enabled"
							checked={constellationState.config?.enabled ?? false}
							disabled={!constellationState.config}
							onchange={(v) => void constellationState.setEnabled(v)}
						/>
					{/snippet}
					{#snippet details()}
						Weaver checks your recent threads on a schedule and builds your personal library of stars.
						Turning this off also hides Constellation from the sidebar. Check interval and model are in
						Constellation's own settings.
					{/snippet}
				</SettingsRow>
				<SettingsRow
					icon={Wrench}
					title="Tools"
					desc="Turn individual tools on or off"
					onclick={() => (showTools = true)}
				/>
			</div>

			<div class="settings-label usage-section-label">System</div>
			<div class="settings-group">
				{#if appState.version}
					<SettingsRow
						icon={appState.deployment === 'docker' ? Container : appState.deployment === 'bare-metal' ? Server : Tag}
						title="Version"
						desc={appState.deployment === 'docker'
							? 'Running in Docker'
							: appState.deployment === 'bare-metal'
								? 'Running bare-metal'
								: undefined}
					>
						{#snippet control()}
							<code class="version">{appState.version}</code>
						{/snippet}
					</SettingsRow>
				{/if}
				<SettingsRow
					icon={RefreshCw}
					title="Update Polaris"
					desc={appState.settings.updateKind === 'update' && appState.settings.updateState === 'updating'
						? 'Pulling & building…'
						: appState.settings.updateKind === 'update' && appState.settings.updateState === 'restarting'
							? 'Restarting…'
							: 'Pull the latest code, rebuild, restart'}
					spin={appState.settings.updateKind === 'update' &&
						(appState.settings.updateState === 'updating' || appState.settings.updateState === 'restarting')}
					chevron={false}
					disabled={appState.settings.updateState !== 'idle' && appState.settings.updateState !== 'error'}
					onclick={() => appState.settings.pushUpdate(() => appState.busy)}
				/>
				<!-- No pull, no rebuild — just kills and cleanly restarts the
				     running binary. Separate from Update Polaris because running
				     the full update flow just to force a restart still does a
				     real (if usually no-op) git pull and go build first, which
				     can stall for no benefit when there's nothing new to pull. -->
				<SettingsRow
					icon={RotateCw}
					title="Restart Polaris"
					desc={appState.settings.updateKind === 'restart' &&
					(appState.settings.updateState === 'updating' || appState.settings.updateState === 'restarting')
						? 'Restarting…'
						: 'Restart only — no pull, no rebuild'}
					spin={appState.settings.updateKind === 'restart' &&
						(appState.settings.updateState === 'updating' || appState.settings.updateState === 'restarting')}
					chevron={false}
					disabled={appState.settings.updateState !== 'idle' && appState.settings.updateState !== 'error'}
					onclick={() => appState.settings.pushRestart(() => appState.busy)}
				/>
			</div>
			{#if appState.settings.updateLog}
				<pre class="log">{appState.settings.updateLog}</pre>
			{/if}
		{/if}
	</div>
</div>

{#if showConstellationUsage}
	<ConstellationUsageModal onClose={() => (showConstellationUsage = false)} />
{/if}

{#if showPulsarUsage}
	<PulsarUsageModal onClose={() => (showPulsarUsage = false)} />
{/if}

{#if showInstructionsWizard}
	<WizardOverlay
		target={{ kind: 'global_instructions' }}
		seed={appState.settings.customInstructions}
		onClose={() => (showInstructionsWizard = false)}
		onAccept={(text) => appState.settings.setCustomInstructions(text)}
	/>
{/if}

<style>
	/* .modal-backdrop/.modal-panel/.modal-panel-header live in app.css —
	   shared with ComposerMenu.svelte, one popup treatment (including the
	   mobile bottom-sheet behavior) for the whole app instead of two
	   copies to keep in sync by hand. Same for .usage-big-cost/
	   .usage-section-label/.usage-stat-group/.usage-stat-row, shared with
	   ConstellationUsageModal.svelte's own Usage section.

	   The main settings view is built from SettingsRow inside one card per
	   group (General / You / Voice / Features / System), labelled with the
	   same small-caps .usage-section-label the Usage view uses. A row is
	   icon + title + at most one dim line; anything longer goes behind the
	   row's "Details" disclosure rather than a hint paragraph, so no section
	   outweighs its neighbours. */

	.header-actions {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
	}

	/* .usage-section-label's own :first-of-type reset never matches here
	   (the header above is also a div), and its --space-lg top margin is
	   tighter than the breathing room between whole groups wants. */
	.settings-label {
		margin-top: var(--space-xl);
	}

	.modal-panel-header + .settings-label {
		margin-top: 0;
	}

	.settings-group {
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		overflow: hidden;
	}

	.settings-group select {
		font: inherit;
		font-size: 13px;
		background: var(--color-surface-3);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
		padding: var(--space-xs) var(--space-sm);
		/* Fixed width so a long model name truncates inside the select
		   instead of squeezing the row's title onto two lines on a phone
		   (a select's intrinsic width is its longest option). */
		width: 10.5rem;
		max-width: 100%;
		text-overflow: ellipsis;
	}

	/* Compact, inline field beside its own row title (Name, Location) —
	   sized like the select above, not stretched full-width the way a
	   stacked field is. */
	.inline-input {
		font: inherit;
		font-size: 13px;
		background: var(--color-surface-3);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
		padding: var(--space-xs) var(--space-sm);
		width: 140px;
		max-width: 100%;
	}

	.usage-empty {
		text-align: center;
		font-size: 13.5px;
		color: var(--color-text-dim);
		padding: var(--space-2xl) 0;
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

	/* 2x2 grid of selectable voice cards — same recessed-track/raised-active
	   language as .theme-toggle, but a grid since four labelled options
	   (flag + name + gender) don't fit one row on a phone. */
	.voice-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: var(--space-xs);
		background: var(--color-bg);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-xs);
	}

	.voice-option {
		display: flex;
		align-items: center;
		border-radius: calc(var(--radius-md) - 3px);
		font-size: 13px;
		color: var(--color-text-dim);
		transition: background-color 0.15s var(--ease-out-expo), color 0.15s var(--ease-out-expo), box-shadow 0.15s var(--ease-out-expo);
	}

	.voice-option:hover {
		color: var(--color-text);
	}

	/* Two sibling buttons (select + preview) inside one card — a button
	   can't nest another button, and tapping play mustn't change the pick. */
	.voice-select {
		flex: 1;
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		border: none;
		background: transparent;
		color: inherit;
		font: inherit;
		padding: var(--space-sm) var(--space-xs) var(--space-sm) var(--space-md);
		min-width: 0;
	}

	.voice-preview {
		display: flex;
		align-items: center;
		justify-content: center;
		border: none;
		background: transparent;
		color: inherit;
		padding: var(--space-sm) var(--space-md) var(--space-sm) var(--space-sm);
	}

	.voice-preview:hover {
		color: var(--color-accent);
	}

	.voice-option.active {
		background: var(--color-surface-3);
		color: var(--color-text);
		font-weight: 600;
		box-shadow: var(--shadow-xs);
	}

	.voice-name {
		flex: 1;
		text-align: left;
	}

	.voice-gender {
		display: flex;
	}

	/* Four presets don't fit one phone-width row beside their label, so the
	   track wraps (and every pill grows to share the row). */
	.person-pronoun-toggle {
		flex-wrap: wrap;
	}

	.person-pronoun-toggle button {
		flex: 1 1 auto;
		justify-content: center;
		padding-inline: var(--space-sm);
	}

	/* Full-width, "carved into the surface" field — used wherever a
	   row's below-content is a free-text field rather than a
	   toggle/segmented-control (custom pronouns). */
	.stacked-input {
		width: 100%;
		margin-top: var(--space-sm);
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

	/* Right-aligned launcher above the textarea; WizardButton carries its
	   own bottom margin, so this row adds none. */
	.wizard-row {
		display: flex;
		justify-content: flex-end;
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

	:global(.spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
