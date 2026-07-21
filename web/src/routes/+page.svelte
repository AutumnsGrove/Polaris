<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import ChatTurnView from '$lib/components/ChatTurnView.svelte';
	import ModelSelector from '$lib/components/ModelSelector.svelte';
	import VoiceButton from '$lib/components/VoiceButton.svelte';
	import { Send, Square, PanelLeft, Gauge, Coins } from '@lucide/svelte';

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

	// Context-usage %, next to thread cost — same threshold the backend
	// auto-compacts at, so this doubles as a warning before that happens.
	let contextPercent = $derived(
		appState.settings.contextWindowTokens > 0
			? Math.min(100, Math.round((appState.contextTokens / appState.settings.contextWindowTokens) * 100))
			: 0
	);
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
		<button
			type={appState.busy ? 'button' : 'submit'}
			class="send-btn"
			class:stop={appState.busy}
			disabled={!appState.busy && !input.trim()}
			title={appState.busy ? 'Stop generating' : 'Send'}
			onclick={() => {
				if (appState.busy) appState.stopGeneration();
			}}
		>
			{#if appState.busy}
				<Square size={14} fill="currentColor" />
			{:else}
				<Send size={16} />
			{/if}
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
		{#if appState.turns.length > 0}
			<div class="context-usage" class:hot={contextPercent >= 90} title="Context window used">
				<Gauge size={12} />
				<span class="label">Context:</span>
				<span class="context-value">{contextPercent}%</span>
			</div>
		{/if}
		{#if appState.settings.showPrices}
			<div class="cost" title="Thread cost">
				<Coins size={12} />
				<span class="label">Thread cost:</span>
				<span class="cost-value">${appState.totalCost.toFixed(4)}</span>
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
		<h1 class="welcome-heading">Ask <span class="wordmark">Polaris</span> anything</h1>
		<p class="subtitle">Your <span class="wordmark">questions</span>, answered with <span class="wordmark">sources</span> from the web.</p>
		<div class="welcome-composer">
			{@render composerForm()}
		</div>
	</div>
{:else}
	<div class="timeline-scroll" bind:this={scrollEl}>
		{#each appState.turns as turn, i (i)}
			<ChatTurnView {turn} index={i} />
		{/each}
		{#if !appState.busy && appState.suggestions.length > 0}
			<div class="suggestions">
				{#each appState.suggestions as suggestion}
					<button class="suggestion-chip" onclick={() => appState.send(suggestion)}>
						{suggestion}
					</button>
				{/each}
			</div>
		{/if}
	</div>
	{@render composerForm()}
{/if}

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		border-bottom: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-surface) 60%, transparent);
		padding: 10px 16px;
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

	.cost,
	.context-usage {
		display: flex;
		align-items: center;
		gap: 4px;
		flex-shrink: 0;
		font-size: 12px;
		color: var(--color-text-dim);
		letter-spacing: 0.01em;
	}

	.context-usage {
		padding-right: 10px;
		border-right: 1px solid var(--color-border);
	}

	.cost-value,
	.context-value {
		color: var(--color-text);
		font-variant-numeric: tabular-nums;
	}

	/* Approaching the auto-compaction threshold — a quiet heads-up before
	   it fires, not an alarm; still just text weight/color, no icon change. */
	.context-usage.hot .context-value {
		color: var(--color-danger);
		font-weight: 600;
	}

	/* Below phone width, the label text ("Context:", "Thread cost:") is
	   the first thing to go — the icon plus value alone still reads fine
	   at a glance, and the header stops fighting the model selector for
	   room. Icons stay so cost and context are still distinguishable. */
	@media (max-width: 480px) {
		.cost .label,
		.context-usage .label {
			display: none;
		}

		.header-right {
			gap: 6px;
		}

		.context-usage {
			padding-right: 6px;
		}
	}

	/* The welcome state is the ONE screen in the app allowed a committed
	   color treatment — a subtle off-center radial wash of the starlight
	   accent behind the heading. Not a card, not glass, not a gradient
	   applied to text. Just a soft distant-sun cast on the ground the
	   composer sits on. Positioned above/left of center so it feels
	   observed rather than staged. */
	.welcome {
		position: relative;
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 6px;
		padding: 48px 24px;
		text-align: center;
		overflow-y: auto;
		isolation: isolate;
	}

	.welcome::before {
		content: '';
		position: absolute;
		inset: 0;
		z-index: -1;
		background:
			radial-gradient(
				ellipse 60% 45% at 38% 34%,
				color-mix(in srgb, var(--color-accent) 22%, transparent) 0%,
				color-mix(in srgb, var(--color-accent) 8%, transparent) 35%,
				transparent 70%
			);
		pointer-events: none;
	}

	:root[data-theme='light'] .welcome::before {
		background:
			radial-gradient(
				ellipse 60% 45% at 38% 34%,
				color-mix(in srgb, var(--color-accent) 14%, transparent) 0%,
				color-mix(in srgb, var(--color-accent) 5%, transparent) 40%,
				transparent 70%
			);
	}

	.welcome-heading {
		margin: 0 0 4px 0;
		font-family: var(--font-serif);
		/* Real hero scale — this is the one heading in the app allowed to
		   run large, since there's no competing content on this screen. */
		font-size: clamp(36px, 6vw, 56px);
		font-weight: 700;
		letter-spacing: -0.02em;
		line-height: 1.1;
		color: var(--color-text);
	}

	.welcome-heading .wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		font-size: 0.88em;
		letter-spacing: 0.01em;
	}

	.welcome .subtitle {
		margin: 10px 0 40px 0;
		color: var(--color-text-dim);
		font-size: 14px;
		line-height: 1.5;
	}

	.welcome .subtitle .wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
	}

	.welcome-composer {
		width: 100%;
		max-width: 640px;
	}

	.welcome-composer :global(.composer) {
		border-top: none;
		padding: 0 0 12px 0;
	}

	/* Composer inside the welcome state gets a touch more presence —
	   a soft accent ring on focus that ties back to the hero glow.
	   Regular in-conversation composer stays plain. */
	.welcome-composer :global(textarea:focus) {
		box-shadow: 0 0 0 4px color-mix(in srgb, var(--color-accent) 18%, transparent);
	}

	.timeline-scroll {
		flex: 1;
		overflow-y: auto;
		padding: 20px 16px;
		display: flex;
		flex-direction: column;
		gap: 18px;
	}

	/* Sits right below the last answer, inside the scrolling timeline —
	   not pinned near the composer, since these are about that specific
	   answer, not a persistent app-level control. */
	.suggestions {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		margin-top: -6px;
	}

	.suggestion-chip {
		border: 1px solid var(--color-border);
		background: var(--color-surface-2);
		color: var(--color-text-dim);
		border-radius: 999px;
		padding: 7px 14px;
		font-size: 12.5px;
		font-family: var(--font-sans);
		text-align: left;
		transition:
			border-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo),
			background-color 0.15s var(--ease-out-expo),
			transform 0.15s var(--ease-out-expo);
	}

	.suggestion-chip:hover {
		border-color: var(--color-accent-2);
		color: var(--color-text);
		background: var(--color-surface-3);
		transform: translateY(-1px);
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
		padding: 10px 14px;
		font-size: 14px;
		line-height: 1.5;
		font-family: var(--font-sans);
		color: var(--color-text);
		outline: none;
		transition: border-color 0.15s var(--ease-out-expo), background-color 0.15s var(--ease-out-expo);
	}

	textarea::placeholder {
		color: var(--color-text-dim);
	}

	textarea:hover {
		border-color: var(--color-border-strong);
	}

	textarea:focus {
		border-color: var(--color-accent);
		background: var(--color-surface);
	}

	.send-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		border: 1px solid transparent;
		background: var(--color-accent);
		color: oklch(18% 0.02 75);
		border-radius: var(--radius-md);
		width: 38px;
		height: 38px;
		box-shadow: 0 1px 2px rgba(15, 10, 5, 0.25);
		transition:
			background-color 0.18s var(--ease-out-expo),
			transform 0.18s var(--ease-out-expo),
			box-shadow 0.18s var(--ease-out-expo),
			opacity 0.15s var(--ease-out-expo);
	}

	:root[data-theme='light'] .send-btn {
		color: oklch(98% 0.005 80);
		box-shadow: 0 1px 2px rgba(60, 48, 32, 0.14);
	}

	.send-btn:hover:not(:disabled) {
		background: var(--color-accent-strong);
		transform: translateY(-1px);
		box-shadow:
			0 6px 16px -6px color-mix(in srgb, var(--color-accent) 55%, transparent),
			0 2px 4px rgba(15, 10, 5, 0.3);
	}

	.send-btn:active:not(:disabled) {
		transform: translateY(0);
		box-shadow: 0 1px 2px rgba(15, 10, 5, 0.25);
	}

	.send-btn:disabled {
		opacity: 0.35;
		cursor: default;
		box-shadow: none;
	}

	/* Stop mode: deliberately not the accent gold — that's reserved for
	   the primary "send" action, and a stop control shouldn't read as
	   another CTA competing with it. A quiet neutral chip that stays
	   legible without stealing attention from the streaming answer. */
	.send-btn.stop {
		background: var(--color-surface-3);
		color: var(--color-text);
		box-shadow: none;
	}

	.send-btn.stop:hover {
		background: var(--color-border-strong);
		transform: none;
		box-shadow: none;
	}
</style>
