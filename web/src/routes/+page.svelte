<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import ChatTurnView from '$lib/components/ChatTurnView.svelte';
	import ModelSelector from '$lib/components/ModelSelector.svelte';
	import { Send } from '@lucide/svelte';

	let input = $state('');
	let scrollEl: HTMLDivElement;

	function submit() {
		const text = input;
		input = '';
		appState.send(text);
	}

	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			submit();
		}
	}

	$effect(() => {
		// Re-run whenever the turn count or streaming content changes.
		appState.turns.length;
		for (const t of appState.turns) t.content;
		queueMicrotask(() => scrollEl?.scrollTo({ top: scrollEl.scrollHeight, behavior: 'smooth' }));
	});

	// Tab title mirrors the current query while a thread is active, Google-style
	// ("query — Polaris Search"), falling back to the plain app name otherwise.
	let pageTitle = $derived.by(() => {
		const lastUser = [...appState.turns].reverse().find((t) => t.role === 'user');
		if (!lastUser?.content) return 'Polaris Search';
		const query = lastUser.content.length > 60 ? lastUser.content.slice(0, 60) + '…' : lastUser.content;
		return `${query} — Polaris Search`;
	});
</script>

<svelte:head>
	<title>{pageTitle}</title>
</svelte:head>

<header class="header">
	<ModelSelector />
	<div class="cost">
		Thread cost: <span class="cost-value">${appState.totalCost.toFixed(4)}</span>
	</div>
</header>

<div class="timeline-scroll" bind:this={scrollEl}>
	{#if appState.turns.length === 0}
		<div class="empty">Ask anything — search and reading happen automatically when needed.</div>
	{/if}
	{#each appState.turns as turn, i (i)}
		<ChatTurnView {turn} />
	{/each}
</div>

<form
	class="composer"
	onsubmit={(e) => {
		e.preventDefault();
		submit();
	}}
>
	<textarea
		placeholder="Ask Polaris…"
		rows="1"
		bind:value={input}
		onkeydown={onKeydown}
	></textarea>
	<button type="submit" class="send-btn" disabled={appState.busy || !input.trim()}>
		<Send size={16} />
	</button>
</form>

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		border-bottom: 1px solid var(--color-border);
		padding: 12px 16px;
	}

	.cost {
		font-size: 13px;
		color: var(--color-text-dim);
	}

	.cost-value {
		color: var(--color-accent);
	}

	.timeline-scroll {
		flex: 1;
		overflow-y: auto;
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	.empty {
		display: flex;
		height: 100%;
		align-items: center;
		justify-content: center;
		color: var(--color-text-dim);
		font-size: 14px;
	}

	.composer {
		display: flex;
		align-items: flex-end;
		gap: 8px;
		border-top: 1px solid var(--color-border);
		padding: 12px;
	}

	textarea {
		flex: 1;
		resize: none;
		border: 1px solid var(--color-border);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: 10px 12px;
		font-size: 14px;
		outline: none;
	}

	textarea:focus {
		border-color: color-mix(in srgb, var(--color-accent-2) 50%, transparent);
	}

	.send-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		border: none;
		background: var(--color-accent-2);
		color: var(--color-bg);
		border-radius: var(--radius-md);
		width: 38px;
		height: 38px;
	}

	.send-btn:disabled {
		opacity: 0.4;
	}
</style>
