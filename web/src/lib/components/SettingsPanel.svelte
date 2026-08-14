<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { X, Moon, Sun, RefreshCw, RotateCw, Info, ChevronLeft } from '@lucide/svelte';
	import { FOCUS_MODES } from '$lib/focusModes';
	import type { FocusMode } from '$lib/types';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';

	function close() {
		appState.settings.open = false;
	}

	// Local, not on SettingsState — unlike updateState (see its own doc
	// comment on why that one has to survive an unmount), there's nothing
	// in-flight to preserve here. The panel always reopens on the normal
	// settings view, which is the right default every time.
	let showStats = $state(false);

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

			{#if appState.settings.usage}
				<section>
					<h3>Last 30 days</h3>
					<div class="row">
						<span>Cost</span>
						<span>${appState.settings.usage.period_cost_usd.toFixed(2)}</span>
					</div>
					<div class="row">
						<span>Threads / turns</span>
						<span>{appState.settings.usage.thread_count} / {appState.settings.usage.turn_count}</span>
					</div>
					<div class="row">
						<span>Tool calls</span>
						<span>{toolCallTotal} ({toolErrorRate.toFixed(1)}% errored)</span>
					</div>
					<div class="row">
						<span>Ran out of turn budget</span>
						<span>{appState.settings.usage.max_turns_wrapup_count} ({wrapupRate.toFixed(1)}% of turns)</span>
					</div>
					<div class="row">
						<span>Check-in nudges</span>
						<span>{appState.settings.usage.check_in_count}</span>
					</div>
					<div class="row">
						<span>Stale-streak warnings</span>
						<span>{appState.settings.usage.stale_streak_count}</span>
					</div>
					<div class="row">
						<span>Auto-compactions</span>
						<span>{appState.settings.usage.compaction_count}</span>
					</div>
					<p class="hint">
						All-time cost: ${appState.settings.usage.total_cost_usd.toFixed(2)}. Run
						<code>polaris stats</code> for the full per-tool breakdown.
					</p>
				</section>
			{:else}
				<section>
					<p class="hint">Loading…</p>
				</section>
			{/if}
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

			<section>
				<h3>Appearance</h3>
				<div class="row">
					<span>Theme</span>
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
			</section>

			<section>
				<h3>Model</h3>
				<div class="row">
					<span>Default model</span>
					<select
						value={appState.settings.defaultModel}
						onchange={(e) => appState.settings.setDefaultModel(e.currentTarget.value, () => appState.loadModels())}
					>
						{#each appState.models as model (model.id)}
							<option value={model.id}>{model.name}</option>
						{/each}
					</select>
				</div>
				<p class="hint">
					Applies to new threads. You can still switch models per-thread from the chat header.
				</p>
			</section>

			<section>
				<h3>Focus</h3>
				<div class="row">
					<span>Default focus mode</span>
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
				<p class="hint">
					Applied to every new message until changed from the composer's "+" menu.
				</p>
			</section>

			<section>
				<h3>Voice</h3>
				<div class="row">
					<span>Mic button</span>
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
				<p class="hint">
					"Tap to toggle" starts recording on the first tap and stops on the second — no need to
					keep a finger down for the whole memo. "Hold to talk" is the original press-and-hold
					behavior.
				</p>
			</section>

			<section>
				<h3>Location</h3>
				<div class="row location-row">
					<input
						type="text"
						placeholder="e.g. Seattle, WA"
						value={appState.settings.manualLocation}
						onchange={(e) => appState.settings.setManualLocation(e.currentTarget.value)}
					/>
				</div>
				<p class="hint">
					Used by "near me" questions when the browser can't get your real location (it needs
					https://, not this app's plain Tailscale IP). Ignored automatically once a real GPS fix
					is available.
				</p>
			</section>

			<section>
				<h3>Updates</h3>
				{#if appState.version}
					<div class="row version-row">
						<span>Version</span>
						<code class="version">{appState.version}</code>
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
							Update Polaris
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
							Restart Polaris
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
			</section>
		{/if}
	</div>
</div>

<style>
	/* .modal-backdrop/.modal-panel/.modal-panel-header live in app.css —
	   shared with ComposerMenu.svelte, one popup treatment (including the
	   mobile bottom-sheet behavior) for the whole app instead of two
	   copies to keep in sync by hand. */

	/* Whitespace does the separating instead of a rule line — a wider gap
	   between sections reads as more considered than a hairline, and pairs
	   with the modal's own step up to a deeper, glass-like surface. */
	section {
		margin-bottom: 26px;
		padding-bottom: 0;
	}

	section:last-child {
		margin-bottom: 0;
	}

	/* Small-caps section labels: heavier weight + wider tracking so the
	   contrast against 400-weight body copy underneath reads as confident
	   rather than timid. Text color pushed up a notch so labels aren't
	   ghost-dim. */
	section h3 {
		margin: 0 0 12px 0;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.12em;
		color: var(--color-text);
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 4px;
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		margin-bottom: 8px;
		font-size: 14px;
	}

	.hint {
		font-size: 12px;
		color: var(--color-text-dim);
		margin: 6px 0 0 0;
	}

	.hint code {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 11px;
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
		gap: 2px;
		background: var(--color-bg);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: 3px;
	}

	.theme-toggle button {
		display: flex;
		align-items: center;
		gap: 6px;
		border: none;
		background: transparent;
		border-radius: calc(var(--radius-md) - 3px);
		padding: 6px 12px;
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

	select {
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: 6px 10px;
		font-size: 13px;
	}

	.location-row input {
		flex: 1;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: 7px 10px;
		font-size: 13px;
		color: var(--color-text);
	}

	.location-row input::placeholder {
		color: var(--color-text-dim);
	}

	.version-row {
		margin-bottom: 12px;
	}

	.version {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 12px;
		color: var(--color-text-dim);
		background: var(--color-bg);
		padding: 4px 8px;
		border-radius: var(--radius-sm);
		border: none;
		box-shadow: var(--shadow-well);
	}

	.update-actions {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.update-btn,
	.restart-btn {
		width: 100%;
	}

	.log {
		margin-top: 12px;
		padding: 10px 12px;
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
