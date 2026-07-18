<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import ChatTurnView from '$lib/components/ChatTurnView.svelte';
	import ModelSelector from '$lib/components/ModelSelector.svelte';
	import VoiceButton from '$lib/components/VoiceButton.svelte';
	import { Send, PanelLeft } from '@lucide/svelte';

	let input = $state('');
	let scrollEl: HTMLDivElement | undefined = $state();

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

{#snippet composerForm()}
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
		<VoiceButton />
		<button type="submit" class="send-btn" disabled={appState.busy || !input.trim()}>
			<Send size={16} />
		</button>
	</form>
{/snippet}

<svelte:head>
	<title>{pageTitle}</title>
</svelte:head>

<header class="header">
	<div class="header-left">
		{#if !appState.sidebarOpen}
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		<ModelSelector />
	</div>
	<div class="header-right">
		{#if appState.showPrices}
			<div class="cost">
				Thread cost: <span class="cost-value">${appState.totalCost.toFixed(4)}</span>
			</div>
		{/if}
	</div>
</header>

{#if appState.turns.length === 0}
	<!-- Empty state: composer floats centered, like Claude/OpenWebUI's
	     landing view, instead of sitting pinned at the bottom of a mostly
	     empty screen. Switches to the normal scrolling-history layout the
	     instant the first message is sent. -->
	<div class="welcome">
		<h1>Ask Polaris anything</h1>
		<p class="subtitle">Search and reading happen automatically when needed.</p>
		<div class="welcome-composer">
			{@render composerForm()}
		</div>
	</div>
{:else}
	<div class="timeline-scroll" bind:this={scrollEl}>
		{#each appState.turns as turn, i (i)}
			<ChatTurnView {turn} index={i} />
		{/each}
	</div>
	{@render composerForm()}
{/if}

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		border-bottom: 1px solid var(--color-border);
		padding: 12px 16px;
		gap: 12px;
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}

	.header-right {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-shrink: 0;
	}

	.cost {
		flex-shrink: 0;
		font-size: 13px;
		color: var(--color-text-dim);
	}

	.cost-value {
		color: var(--color-accent);
	}

	.welcome {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 4px;
		padding: 24px;
		text-align: center;
		overflow-y: auto;
	}

	.welcome h1 {
		margin: 0;
		font-size: 22px;
		font-weight: 600;
	}

	.welcome .subtitle {
		margin: 0 0 20px 0;
		color: var(--color-text-dim);
		font-size: 14px;
	}

	.welcome-composer {
		width: 100%;
		max-width: 640px;
	}

	.welcome-composer :global(.composer) {
		border-top: none;
		padding-bottom: 12px;
	}

	.timeline-scroll {
		flex: 1;
		overflow-y: auto;
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	.composer {
		display: flex;
		align-items: flex-end;
		gap: 8px;
		border-top: 1px solid var(--color-border);
		padding: 12px;
		/* Clears iOS Safari's bottom toolbar / home-indicator area — falls
		   back to the plain 12px on browsers without safe-area support. */
		padding-bottom: max(12px, env(safe-area-inset-bottom));
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
