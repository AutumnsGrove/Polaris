<script lang="ts">
	import type { ChatTurn } from '$lib/types';
	import { appState } from '$lib/state.svelte';
	import { requestFreshLocation } from '$lib/geolocation';
	import { Send, MapPin, Globe, Loader2, Check, Users } from '@lucide/svelte';

	// isLast: only the thread's current last turn gets working controls —
	// any later message already implies this question is resolved
	// (answering it is just an ordinary chat message, see
	// tools/registry.go's PendingQuestion doc comment). answeredWith is
	// that next message's content, if there is one — used to render a
	// static, resolved view (which option was picked) instead of the
	// question disappearing entirely once it's no longer live. undefined
	// means either isLast (nothing to resolve with yet) or, on a genuinely
	// interrupted thread, that no answer ever came. noResearch is the
	// composer's current Research toggle (inverted) — an ordinary answer
	// (option pick, freeform text, shared location) should preserve
	// whatever chat-mode state the user currently has, not silently flip
	// research back on; only enableWebSearch() below is meant to override it.
	let {
		turn,
		isLast,
		answeredWith,
		noResearch
	}: { turn: ChatTurn; isLast: boolean; answeredWith?: string; noResearch: boolean } = $props();

	// Exact match against the offered options — a freeform reply (typed
	// instead of tapped, or a shared location) won't match any of them,
	// which is a real, valid outcome: the resolved view falls back to
	// showing the raw answer text instead of a false-highlighted option.
	let matchedOption = $derived(
		answeredWith !== undefined ? turn.pendingQuestion?.options?.find((o) => o === answeredWith) : undefined
	);

	let freeform = $state('');
	let locatingInProgress = $state(false);

	// noResearchOverride lets enableWebSearch() below force research back on
	// for just this one reply; every other caller falls through to the
	// composer's current toggle state instead of always sending false.
	function answer(text: string, noResearchOverride?: boolean) {
		const trimmed = text.trim();
		if (!trimmed || appState.busy) return;
		appState.send(trimmed, undefined, undefined, undefined, undefined, noResearchOverride ?? noResearch);
		freeform = '';
	}

	function submitFreeform() {
		answer(freeform);
	}

	async function shareLocation() {
		if (locatingInProgress || appState.busy) return;
		locatingInProgress = true;
		try {
			const loc = await requestFreshLocation();
			if (loc) answer(loc);
		} finally {
			locatingInProgress = false;
		}
	}

	// Chat mode (composer's Research toggle off) hid the research tools
	// from this turn entirely, so the model can't just try web_search and
	// fail — it asked first, via wants_web_search. Explicitly overriding
	// noResearch to false here (unlike every other answer() call, which
	// falls through to the composer's current toggle) sends this one reply
	// with research enabled regardless of the composer's own toggle state.
	// That's deliberately scoped to just this one follow-up: it doesn't
	// flip the composer's Research switch back on for later turns.
	function enableWebSearch() {
		answer('Yes, please search the web for this.', false);
	}
</script>

{#if turn.pendingQuestion && isLast}
	<div class="question-card">
		{#if turn.pendingQuestion.plan?.sub_agent_objectives.length}
			<div class="plan">
				<div class="plan-header">
					<Users size={13} />
					<span>Proposed research plan</span>
				</div>
				<ol class="plan-objectives">
					{#each turn.pendingQuestion.plan.sub_agent_objectives as objective (objective)}
						<li>{objective}</li>
					{/each}
				</ol>
				{#if turn.pendingQuestion.plan.estimated_search_calls}
					<p class="plan-estimate">~{turn.pendingQuestion.plan.estimated_search_calls} searches estimated</p>
				{/if}
			</div>
		{/if}

		{#if turn.pendingQuestion.options?.length}
			<div class="options">
				{#each turn.pendingQuestion.options as option, i (option)}
					<button class="option-row" onclick={() => answer(option)} disabled={appState.busy}>
						<span class="option-index">{i + 1}</span>
						<span class="option-text">{option}</span>
					</button>
				{/each}
			</div>
		{/if}

		{#if turn.pendingQuestion.wants_location}
			<button class="location-action" onclick={shareLocation} disabled={locatingInProgress || appState.busy}>
				{#if locatingInProgress}
					<Loader2 size={14} class="spin" />
				{:else}
					<MapPin size={14} />
				{/if}
				<span>Share my location</span>
			</button>
		{/if}

		{#if turn.pendingQuestion.wants_web_search}
			<button class="location-action" onclick={enableWebSearch} disabled={appState.busy}>
				<Globe size={14} />
				<span>Enable web search</span>
			</button>
		{/if}

		<form
			class="freeform"
			onsubmit={(e) => {
				e.preventDefault();
				submitFreeform();
			}}
		>
			<input
				class="freeform-input"
				type="text"
				placeholder="Type your own answer…"
				bind:value={freeform}
				disabled={appState.busy}
			/>
			<button class="freeform-send" type="submit" disabled={appState.busy || !freeform.trim()}>
				<Send size={14} />
			</button>
		</form>
	</div>
{:else if turn.pendingQuestion && answeredWith !== undefined}
	<!-- Resolved: the question already got a real answer (the next message
	     in the thread), so this renders as plain historical record — the
	     same options list, but inert, with whichever one matches the
	     answer picked out. No location/freeform controls; there's nothing
	     left to do here. -->
	<div class="question-card resolved">
		{#if turn.pendingQuestion.plan?.sub_agent_objectives.length}
			<div class="plan">
				<div class="plan-header">
					<Users size={13} />
					<span>Proposed research plan</span>
				</div>
				<ol class="plan-objectives">
					{#each turn.pendingQuestion.plan.sub_agent_objectives as objective (objective)}
						<li>{objective}</li>
					{/each}
				</ol>
				{#if turn.pendingQuestion.plan.estimated_search_calls}
					<p class="plan-estimate">~{turn.pendingQuestion.plan.estimated_search_calls} searches estimated</p>
				{/if}
			</div>
		{/if}

		{#if turn.pendingQuestion.options?.length}
			<div class="options">
				{#each turn.pendingQuestion.options as option, i (option)}
					<div class="option-row" class:picked={option === matchedOption}>
						{#if option === matchedOption}
							<span class="option-index picked-index"><Check size={12} /></span>
						{:else}
							<span class="option-index">{i + 1}</span>
						{/if}
						<span class="option-text">{option}</span>
					</div>
				{/each}
			</div>
		{/if}
		{#if matchedOption === undefined}
			<!-- A freeform reply or a shared location matches none of the
			     offered options — still worth showing what was actually
			     answered, rather than leaving the resolved card silent
			     about it. -->
			<p class="answered-freeform">Answered: {answeredWith}</p>
		{/if}
	</div>
{/if}

<style>
	.question-card {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		margin-top: var(--space-md);
		padding: var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
	}

	.options {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		overflow: hidden;
	}

	.plan {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		padding: var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
	}

	.plan-header {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		color: var(--color-text-dim);
		font-size: 12px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.02em;
	}

	.plan-objectives {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		margin: 0;
		padding-left: var(--space-lg);
		color: var(--color-text);
		font-size: 13.5px;
	}

	.plan-estimate {
		margin: 0;
		color: var(--color-text-dim);
		font-size: 12px;
	}

	.option-row {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		width: 100%;
		padding: var(--space-md);
		border: none;
		border-bottom: 1px solid var(--color-border);
		background: var(--color-surface);
		color: var(--color-text);
		font-family: var(--font-sans);
		font-size: 13.5px;
		text-align: left;
		transition:
			background-color 0.15s var(--ease-out-expo),
			transform 0.15s var(--ease-out-expo);
	}

	.option-row:last-child {
		border-bottom: none;
	}

	.option-row:hover:not(:disabled) {
		background: var(--color-surface-3);
	}

	.option-row:active:not(:disabled) {
		transform: scale(0.995);
	}

	.option-row:disabled {
		opacity: 0.5;
		cursor: default;
	}

	.option-index {
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		width: 20px;
		height: 20px;
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		color: var(--color-text-dim);
		font-size: 11px;
		font-weight: 600;
	}

	/* Resolved (historical) rendering — same list, but inert: dimmed
	   overall, with the one that was actually picked brought back to full
	   opacity and marked with a check instead of its number. */
	.question-card.resolved .option-row {
		cursor: default;
		opacity: 0.55;
	}

	.question-card.resolved .option-row.picked {
		opacity: 1;
	}

	.picked-index {
		background: var(--color-accent);
		color: oklch(18% 0.02 75);
	}

	:root[data-theme='light'] .picked-index {
		color: oklch(98% 0.005 80);
	}

	.answered-freeform {
		margin: 0;
		padding: var(--space-sm) var(--space-md);
		font-size: 12.5px;
		font-style: italic;
		color: var(--color-text-dim);
	}

	.location-action {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		align-self: flex-start;
		padding: var(--space-sm) var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-full);
		background: var(--color-surface);
		color: var(--color-accent);
		font-family: var(--font-sans);
		font-size: 12.5px;
		font-weight: 500;
		transition:
			background-color 0.15s var(--ease-out-expo),
			transform 0.15s var(--ease-out-expo);
	}

	.location-action:hover:not(:disabled) {
		background: var(--color-surface-3);
		transform: translateY(-1px);
	}

	.location-action:disabled {
		opacity: 0.6;
		cursor: default;
	}

	.location-action :global(.spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	.freeform {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.freeform-input {
		flex: 1;
		padding: var(--space-sm) var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-full);
		background: var(--color-surface);
		color: var(--color-text);
		font-family: var(--font-sans);
		font-size: 13px;
	}

	.freeform-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.freeform-send {
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		width: 32px;
		height: 32px;
		border: none;
		border-radius: var(--radius-full);
		background: var(--color-accent);
		color: oklch(18% 0.02 75);
		transition:
			background-color 0.15s var(--ease-out-expo),
			opacity 0.15s var(--ease-out-expo);
	}

	:root[data-theme='light'] .freeform-send {
		color: oklch(98% 0.005 80);
	}

	.freeform-send:hover:not(:disabled) {
		background: var(--color-accent-strong);
	}

	.freeform-send:disabled {
		opacity: 0.35;
		cursor: default;
	}
</style>
