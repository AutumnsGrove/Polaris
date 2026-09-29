<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import type { ChatTurn, FocusMode } from '$lib/types';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { X } from '@lucide/svelte';
	import Asterism from './Asterism.svelte';
	import { CHECK_DISPLAY, CHIP_NAMES, checkStateLabel, optionLabel } from '$lib/oracleLabels';

	// The "why" sheet (docs/plans/oracle-mode.md's C1/4b) — answer stats
	// first (shown regardless of whether Oracle ran this turn), then one
	// card per fired Oracle check with its full option odds, then a
	// collapsed quiet-list for everything that didn't fire. Modeled on
	// ConstellationUsageModal.svelte's shell (onClose prop, shared
	// .modal-backdrop/.modal-panel/.sheet-handle classes) — no dedicated
	// Sheet component exists in this codebase, that's the convention.
	let { turn, index, onClose }: { turn: ChatTurn; index: number; onClose: () => void } = $props();

	function formatDuration(ms: number): string {
		const seconds = ms / 1000;
		if (seconds < 1) return `${Math.round(ms)}ms`;
		if (seconds < 60) return `${seconds.toFixed(1)}s`;
		const minutes = Math.floor(seconds / 60);
		return `${minutes}m ${Math.round(seconds % 60)}s`;
	}

	let modelName = $derived(
		turn.appliedModel
			? (appState.models.find((m) => m.id === turn.appliedModel)?.name ?? turn.appliedModel)
			: undefined
	);

	let checks = $derived(turn.oracleResult?.checks ?? []);
	// "Changed something" (a card) vs. "ran but did nothing" (the collapsed
	// list) — the plan's split. `fired` alone isn't that: a check whose
	// confident winner is the no-op option (high_stakes "none", intent
	// "general", clarify "no") clears its threshold too but injects nothing,
	// and used to get a card labeled "Nudged" with no nudge under it. Focus
	// counts when it fired, since a pick or a clear is itself the change.
	function changedSomething(key: string, check: { fired: boolean; nudge?: string }): boolean {
		return check.fired && (key === 'focus' || !!check.nudge);
	}
	let firedChecks = $derived(
		CHECK_DISPLAY.map((d) => ({ display: d, check: checks.find((c) => c.key === d.key) })).filter(
			(x) => x.check && changedSomething(x.display.key, x.check)
		)
	);
	let quietChecks = $derived(
		CHECK_DISPLAY.map((d) => ({ display: d, check: checks.find((c) => c.key === d.key) })).filter(
			(x) => x.check && !changedSomething(x.display.key, x.check)
		)
	);
	let hasOracleSection = $derived(checks.length > 0);

	let offersLabel = $derived.by(() => {
		const chips = turn.oracleResult?.chips;
		if (!chips?.length) return 'None';
		return chips.map((c) => (c.key === 'field' ? (c.label ?? 'Field') : (CHIP_NAMES[c.key] ?? c.key))).join(', ');
	});

	// The check's own runner-up option (highest probability that isn't the
	// winner) — "Rerun as X" offers trying that specific alternative
	// instead of a generic list of every option, same as the mockup's
	// single button.
	function runnerUp(probabilities: Record<string, number>, winner: string): string | undefined {
		let best: string | undefined;
		let bestP = -1;
		for (const [option, p] of Object.entries(probabilities)) {
			if (option === winner) continue;
			if (p > bestP) {
				best = option;
				bestP = p;
			}
		}
		return best;
	}

	function rerunAs(focusMode: string) {
		appState.retry(index, focusMode as FocusMode);
		onClose();
	}

	// Whether Oracle's focus pick for this turn actually took effect. It
	// can fire and still not apply: "manual always wins" (see the plan
	// doc) means an operator pick for this message keeps the turn on that
	// mode, and `focus.fired` is only Oracle's confidence the winning mode
	// fits — not a claim it was applied. Rendering an overridden pick as
	// "Set" is exactly what made a live session read "Oracle set Academic"
	// while the answer had actually run in the operator's own Shopper mode.
	function focusPickApplied(check: { winner: string }): boolean {
		return turn.oracleFocusModeSource === 'oracle' && turn.appliedFocusMode === check.winner;
	}

	// The focus mode Oracle cleared this turn, for the "cleared" explanation
	// below — the nearest earlier turn's own applied mode, the same
	// backward scan ChatTurnView does for its "Switched X -> Y" note (the
	// turn itself has no mode to read: clearing is what left it empty).
	let clearedFrom = $derived.by(() => {
		if (!turn.oracleResult?.focus_cleared) return undefined;
		for (let i = index - 1; i >= 0; i--) {
			const mode = appState.turns[i]?.appliedFocusMode;
			if (mode) return mode;
		}
		return undefined;
	});

	// The check header's right-hand state label — "Set"/"Nudged" for a real
	// result, plus the two cases those words would misrepresent: an
	// overridden pick (a manual choice won — see focusPickApplied above)
	// and a cleared one (Oracle retracted its own earlier mode). The
	// backend's focus_cleared flag is what distinguishes the two; without
	// it, a cleared pick would render as either "Set" or "Not applied",
	// both wrong.
	function stateLabel(display: { key: string }, check: { winner: string; fired: boolean }): string {
		if (display.key === 'focus' && check.fired) {
			if (turn.oracleResult?.focus_cleared) return 'Cleared';
			return focusPickApplied(check) ? 'Set' : 'Not applied';
		}
		return checkStateLabel(display.key, check.fired);
	}

	function rerunWithoutOracle() {
		appState.retry(index, undefined, true);
		onClose();
	}

	let costTotal = $derived((turn.costAnswer ?? 0) + (turn.costVerification ?? 0) + (turn.costOracle ?? 0));
	// The headline is the tiers' sum when there is one: turn.costUsd on a
	// live turn is only what the 'done' event carried, while a reloaded turn
	// reads the DB's running total — the sum reads the same either way.
	let costHeadline = $derived(costTotal > 0 ? costTotal : (turn.costUsd ?? 0));
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel sheet-panel" role="dialog" aria-modal="true" aria-label="Turn info">
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>This answer</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		<div class="stats">
			<div class="stat-cell wide"><span class="k">Model</span><span class="v">{modelName ?? '—'}</span></div>
			<div class="stat-cell">
				<span class="k">First token</span>
				<span class="v">{turn.ttftMs !== undefined ? `${(turn.ttftMs / 1000).toFixed(1)}s` : '—'}</span>
			</div>
			<div class="stat-cell">
				<span class="k">Speed</span>
				<span class="v"
					>{turn.tokensPerSecond !== undefined ? `${Math.round(turn.tokensPerSecond)}` : '—'}<small>
						{turn.tokensPerSecond !== undefined ? ' tok/s' : ''}</small
					></span
				>
			</div>
			<div class="stat-cell">
				<span class="k">Tokens in</span>
				<span class="v">
					{#if turn.promptTokens !== undefined}
						{turn.promptTokens.toLocaleString()}{#if turn.cacheReadTokens}<small
								>&nbsp;· {Math.round((turn.cacheReadTokens / turn.promptTokens) * 100)}% cached</small
							>{/if}
					{:else}
						—
					{/if}
				</span>
			</div>
			<div class="stat-cell">
				<span class="k">Tokens out</span>
				<span class="v">{turn.completionTokens !== undefined ? turn.completionTokens.toLocaleString() : '—'}</span>
			</div>
			<div class="stat-cell">
				<span class="k">Total time</span>
				<span class="v">{turn.durationMs !== undefined ? formatDuration(turn.durationMs) : '—'}</span>
			</div>
			<div class="stat-cell"><span class="k">Tool calls</span><span class="v">{turn.toolCallCount ?? 0}</span></div>
			{#if turn.costUsd !== undefined}
				<div class="stat-cell wide cost-cell">
					<div class="cost-head"><span class="k">Cost</span><span class="v">${costHeadline.toFixed(5)}</span></div>
					{#if costTotal > 0}
						<div class="cost-bar" aria-hidden="true">
							<i class="t-answer" style="flex:{Math.max(turn.costAnswer ?? 0, 0.0000001)}"></i>
							{#if turn.costVerification}<i class="t-verify" style="flex:{turn.costVerification}"></i>{/if}
							{#if turn.costOracle}<i class="t-oracle" style="flex:{turn.costOracle}"></i>{/if}
						</div>
					{/if}
					<div class="cost-row">
						<span class="dot t-answer"></span>Answer<span class="sub">model + tools</span><span class="amt"
							>${(turn.costAnswer ?? 0).toFixed(5)}</span
						>
					</div>
					{#if turn.costVerification}
						<div class="cost-row">
							<span class="dot t-verify"></span>Verification<span class="sub"
								>Jev · {turn.verification?.length ?? 0} verified</span
							><span class="amt">${turn.costVerification.toFixed(5)}</span>
						</div>
					{/if}
					{#if turn.costOracle}
						<div class="cost-row">
							<span class="dot t-oracle"></span>Oracle<span class="sub">Jev · {checks.length} checks</span
							><span class="amt">${turn.costOracle.toFixed(5)}</span>
						</div>
					{/if}
				</div>
			{/if}
		</div>

		{#if hasOracleSection}
			<div class="group-title"><Asterism size={14} class="o-icon" />Oracle</div>

			{#each firedChecks as { display, check } (display.key)}
				{#if check}
					{@const sortedOptions = Object.entries(check.probabilities).sort((a, b) => b[1] - a[1])}
					<div class="check">
						<div class="check-head">
							<span class="check-name">{display.name}</span>
							<span class="check-state fired">{stateLabel(display, check)}</span>
						</div>
						{#each sortedOptions as [option, p] (option)}
							<div class="opt" class:win={option === check.winner}>
								<span class="label">{optionLabel(display.key, option)}</span>
								<span class="pct">{Math.round(p * 100)}%</span>
								<span class="bar"><i style="width:{Math.round(p * 100)}%"></i></span>
							</div>
						{/each}
						{#if check.nudge}
							<div class="nudge-text">{check.nudge}</div>
						{/if}
						{#if display.key === 'focus'}
							{@const alt = runnerUp(check.probabilities, check.winner)}
							{#if turn.oracleResult?.focus_cleared}
								<div class="nudge-text">
									Cleared{clearedFrom ? ` the earlier ${optionLabel('focus', clearedFrom)} mode` : ''} — no mode
									clearly fits this message, so the thread runs without one.
								</div>
							{:else if !focusPickApplied(check)}
								<div class="nudge-text">
									Not applied — {turn.appliedFocusMode
										? `your own ${optionLabel('focus', turn.appliedFocusMode)} pick for this message`
										: 'your own pick for this message'} took precedence.
								</div>
							{/if}
							{#if alt}
								<button class="rerun" type="button" onclick={() => rerunAs(alt)}>
									Rerun as {optionLabel('focus', alt)}
								</button>
							{/if}
						{/if}
					</div>
				{/if}
			{/each}

			{#if quietChecks.length}
				<div class="quiet-list">
					{#each quietChecks as { display, check } (display.key)}
						{#if check}
							<div>
								<span>{display.name}</span>
								<span>{optionLabel(display.key, check.winner)} · {Math.round((check.probabilities[check.winner] ?? 0) * 100)}%{check.suppressed ? ' · held back' : ''}</span>
							</div>
						{/if}
					{/each}
					<div><span>Offers</span><span>{offersLabel}</span></div>
				</div>
			{/if}

			<button class="rerun" type="button" onclick={rerunWithoutOracle}>Rerun without Oracle</button>
		{/if}
	</div>
</div>

<style>
	.sheet-panel {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
	}

	.stats {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: var(--space-sm);
	}

	.stat-cell {
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: var(--space-sm) var(--space-md);
		display: flex;
		flex-direction: column;
	}

	.stat-cell .k {
		font-size: 11px;
		color: var(--color-text-dim);
	}

	.stat-cell .v {
		font-size: 15px;
		font-weight: 500;
		font-variant-numeric: tabular-nums;
		overflow-wrap: break-word;
	}

	.stat-cell .v small {
		font-size: 11px;
		color: var(--color-text-dim);
		font-weight: 400;
	}

	.stat-cell.wide {
		grid-column: 1 / -1;
	}

	.cost-cell {
		gap: var(--space-xs);
	}

	.cost-head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
	}

	.cost-bar {
		display: flex;
		gap: 2px;
		height: 5px;
		border-radius: var(--radius-full);
		overflow: hidden;
		margin-block: var(--space-xs);
	}

	.cost-bar i {
		display: block;
		min-width: 4px;
	}

	.t-answer {
		background: var(--color-text-dim);
	}

	.t-verify {
		background: var(--color-accent-2);
	}

	.t-oracle {
		background: var(--color-accent);
	}

	.cost-row {
		display: grid;
		grid-template-columns: auto auto 1fr auto;
		align-items: baseline;
		gap: var(--space-sm);
		font-size: 12.5px;
	}

	.cost-row .dot {
		width: 7px;
		height: 7px;
		border-radius: var(--radius-full);
		align-self: center;
	}

	.cost-row .sub {
		font-size: 11px;
		color: var(--color-text-dim);
	}

	.cost-row .amt {
		font-variant-numeric: tabular-nums;
	}

	.group-title {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: 11.5px;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-dim);
		font-weight: 600;
	}

	.group-title :global(.o-icon) {
		color: var(--color-accent);
	}

	.check {
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: var(--space-md);
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		margin-top: var(--space-sm);
	}

	.check-head {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-sm);
	}

	.check-name {
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.check-state {
		font-size: 11px;
		font-weight: 600;
		border-radius: var(--radius-full);
		padding: 0 var(--space-sm);
		color: var(--color-accent);
		background: var(--color-accent-soft);
	}

	.opt {
		display: grid;
		grid-template-columns: 1fr 40px;
		gap: var(--space-xs) var(--space-sm);
		align-items: center;
		font-size: 13px;
	}

	.opt .bar {
		grid-column: 1 / -1;
		height: 4px;
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		overflow: hidden;
	}

	.opt .bar i {
		display: block;
		height: 100%;
		border-radius: inherit;
		background: var(--color-border-strong);
	}

	.opt.win .bar i {
		background: var(--color-accent);
	}

	.opt.win .label {
		font-weight: 500;
	}

	.opt .pct {
		text-align: right;
		font-size: 12px;
		color: var(--color-text-dim);
		font-variant-numeric: tabular-nums;
	}

	.opt:not(.win) .label {
		color: var(--color-text-dim);
	}

	.nudge-text {
		font-size: 12px;
		color: var(--color-text-dim);
		border-left: 2px solid var(--color-accent-soft-strong);
		padding-left: var(--space-sm);
		line-height: 1.5;
	}

	.rerun {
		align-self: flex-start;
		font-size: 12px;
		color: var(--color-text);
		background: var(--color-surface-3);
		border: none;
		border-radius: var(--radius-full);
		padding: var(--space-xs) var(--space-md);
		margin-top: var(--space-xs);
	}

	.quiet-list {
		display: flex;
		flex-direction: column;
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		margin-top: var(--space-sm);
	}

	.quiet-list div {
		display: flex;
		justify-content: space-between;
		font-size: 12.5px;
		padding: var(--space-sm) var(--space-md);
		border-top: 1px solid var(--color-border);
	}

	.quiet-list div:first-child {
		border-top: none;
	}

	.quiet-list span:last-child {
		color: var(--color-text-dim);
		font-variant-numeric: tabular-nums;
	}
</style>
