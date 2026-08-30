<script lang="ts">
	import type { ChatTurn } from '$lib/types';
	import { appState } from '$lib/state.svelte';
	import { requestFreshLocation } from '$lib/geolocation';
	import { Send, MapPin, Loader2 } from '@lucide/svelte';

	// isLast: only the thread's current last turn gets working controls —
	// any later message already implies this question is resolved
	// (answering it is just an ordinary chat message, see
	// tools/registry.go's PendingQuestion doc comment), so an older one in
	// history renders as plain inert text instead of stale, unusable
	// buttons.
	let { turn, isLast }: { turn: ChatTurn; isLast: boolean } = $props();

	let freeform = $state('');
	let locatingInProgress = $state(false);

	function answer(text: string) {
		const trimmed = text.trim();
		if (!trimmed || appState.busy) return;
		appState.send(trimmed);
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
</script>

{#if turn.pendingQuestion && isLast}
	<div class="question-card">
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
