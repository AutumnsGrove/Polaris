<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import ChatTurnView from '$lib/components/ChatTurnView.svelte';
	import ComposerMenu from '$lib/components/ComposerMenu.svelte';
	import VoiceButton from '$lib/components/VoiceButton.svelte';
	import { Send, Square, PanelLeft, Gauge, Coins, Paperclip, X, Loader2, ArrowDown } from '@lucide/svelte';
	import { autoResize } from '$lib/actions/autoResize';
	import { uploadAttachment } from '$lib/upload';
	import ThreadMenu from '$lib/components/ThreadMenu.svelte';
	import type { FocusMode } from '$lib/types';

	let input = $state('');
	let scrollEl: HTMLDivElement | undefined = $state();

	// pinnedToBottom tracks whether the timeline should keep auto-scrolling
	// as new content streams in, vs. leaving the view alone because the
	// user scrolled up to read something. Without this, every streamed
	// token yanked the view back to the bottom — reading a long answer as
	// it streamed in was impossible, since scrolling up got immediately
	// overridden by the next token's auto-scroll (see the content $effect
	// below). "Near the bottom" (not "exactly" — smooth-scroll animations
	// and sub-pixel layout mean it rarely lands on precisely 0) counts as
	// still pinned, so a user who's basically caught up doesn't have to be
	// pixel-perfect to stay auto-scrolling.
	let pinnedToBottom = $state(true);
	const bottomPinThresholdPx = 120;

	function handleTimelineScroll() {
		if (!scrollEl) return;
		const distanceFromBottom = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight;
		pinnedToBottom = distanceFromBottom < bottomPinThresholdPx;
	}

	// Explicit "jump to latest" action (button below) — re-pins and
	// scrolls immediately, same as arriving at a freshly opened thread.
	function scrollToBottom() {
		pinnedToBottom = true;
		scrollEl?.scrollTo({ top: scrollEl.scrollHeight, behavior: 'smooth' });
	}

	// Composer-only state — focusMode/deepResearch ride along in every
	// send() call already; attachedFile gets uploaded (see submit below)
	// only at send time, not the instant it's picked, so backing out of a
	// message with the file still attached never orphans an upload nobody
	// ends up sending.
	let focusMode = $state<FocusMode>('off');
	let deepResearch = $state(false);
	let attachedFile = $state<File | null>(null);
	let uploading = $state(false);

	// Applies the Settings panel's standing default focus mode exactly
	// once, the moment it's actually loaded (settings.load() is async,
	// fired from +layout.svelte's onMount — this component can easily
	// render before it resolves). Guarded so it never overwrites a
	// manual choice made from the composer's "+" menu afterward; "off"
	// is itself a valid loaded value, which is why this checks
	// settings.loaded rather than the value of defaultFocusMode itself.
	let focusModeInitialized = false;
	$effect(() => {
		if (appState.settings.loaded && !focusModeInitialized) {
			focusMode = appState.settings.defaultFocusMode;
			focusModeInitialized = true;
		}
	});

	function handleAttach(file: File) {
		attachedFile = file;
	}

	async function submit() {
		const text = input;
		const file = attachedFile;
		input = '';
		attachedFile = null;

		if (!file) {
			appState.send(text, undefined, focusMode, deepResearch);
			return;
		}

		uploading = true;
		const uploaded = await uploadAttachment(file);
		uploading = false;
		// Falls back to sending the text alone on a failed upload — an
		// error toast would be nicer, but silently dropping the whole
		// message because the attachment failed is worse than answering
		// without it.
		appState.send(text, undefined, focusMode, deepResearch, uploaded ?? undefined);
	}

	// The active thread's title, shown in the header now that the model
	// selector has moved into the composer's "+" sheet — falls back to
	// nothing for a brand-new thread whose title hasn't loaded yet (or
	// hasn't been generated server-side).
	let currentThreadTitle = $derived(
		appState.threads.find((t) => t.id === appState.currentThreadId)?.title ?? ''
	);

	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			submit();
		}
	}

	// Re-pins to the bottom whenever a new turn is appended (a fresh
	// question, or a retry/edit) or a different thread is opened — both
	// should always land on the latest content regardless of where a
	// previous read session left the scroll position. A turn count that
	// only decreases (e.g. DeleteMessagesFrom on edit) doesn't re-pin by
	// itself; the thread-id branch below covers a full thread switch.
	let prevTurnCount = 0;
	$effect(() => {
		if (appState.turns.length > prevTurnCount) pinnedToBottom = true;
		prevTurnCount = appState.turns.length;
	});
	$effect(() => {
		appState.currentThreadId;
		pinnedToBottom = true;
	});

	$effect(() => {
		// Re-run whenever the turn count or streaming content changes —
		// but only actually scroll while pinnedToBottom: this is what lets
		// a user scroll up mid-stream to read from the top without the
		// next token yanking them back down. handleTimelineScroll updates
		// pinnedToBottom as the user scrolls; this effect just respects it.
		appState.turns.length;
		for (const t of appState.turns) t.content;
		if (pinnedToBottom) {
			queueMicrotask(() => scrollEl?.scrollTo({ top: scrollEl.scrollHeight, behavior: 'smooth' }));
		}
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

	// A turn is streaming, but for some other thread — the composer here
	// still can't send (only one turn runs at a time per connection,
	// regardless of thread), but showing a "Stop" control that would
	// actually cancel a different, unrelated thread would be actively
	// wrong, not just unhelpful. See appState.busyOnCurrentThread.
	let busyElsewhere = $derived(appState.busy && !appState.busyOnCurrentThread);
</script>

{#snippet composerForm()}
	<form
		class="composer"
		onsubmit={(e) => {
			e.preventDefault();
			submit();
		}}
	>
		<div class="textarea-wrap">
			{#if !input}
				<!-- A native placeholder attribute can't mix fonts within its
				     text, so the "Polaris" wordmark treatment used everywhere
				     else (see .welcome-heading .wordmark) needs this overlay
				     instead — invisible to interaction (pointer-events: none)
				     and hidden the instant there's real input, so it never
				     competes with what's actually being typed. -->
				<div class="fake-placeholder" aria-hidden="true">
					Ask <span class="wordmark">Polaris</span>…
				</div>
			{/if}
			<textarea
				rows="1"
				bind:value={input}
				onkeydown={onKeydown}
				use:autoResize={{ value: input, maxHeight: 200 }}
				aria-label="Ask Polaris"
			></textarea>
		</div>

		{#if attachedFile}
			<div class="attachment-chip">
				<Paperclip size={13} />
				<span class="attachment-name">{attachedFile.name}</span>
				<button
					type="button"
					onclick={() => (attachedFile = null)}
					disabled={uploading}
					aria-label="Remove attachment"
				>
					<X size={13} />
				</button>
			</div>
		{/if}

		<div class="composer-toolbar">
			<ComposerMenu bind:focusMode bind:deepResearch onAttach={handleAttach} />
			<div class="toolbar-spacer"></div>
			<VoiceButton />
			<button
				type={appState.busyOnCurrentThread ? 'button' : 'submit'}
				class="send-btn"
				class:stop={appState.busyOnCurrentThread}
				disabled={uploading || busyElsewhere || (!appState.busyOnCurrentThread && !input.trim())}
				title={appState.busyOnCurrentThread
					? 'Stop generating'
					: busyElsewhere
						? 'A response is still generating in another thread'
						: uploading
							? 'Uploading…'
							: 'Send'}
				onclick={() => {
					if (appState.busyOnCurrentThread) appState.stopGeneration();
				}}
			>
				{#if appState.busyOnCurrentThread}
					<Square size={14} fill="currentColor" />
				{:else if uploading}
					<Loader2 size={14} class="spin" />
				{:else}
					<Send size={16} />
				{/if}
			</button>
		</div>
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
		{#if currentThreadTitle}
			<h1 class="thread-title" title={currentThreadTitle}>{currentThreadTitle}</h1>
		{/if}
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
		{#if appState.currentThreadId}
			<ThreadMenu threadId={appState.currentThreadId} threadTitle={currentThreadTitle} />
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
		<p class="subtitle wordmark">Your questions, answered with sources from the web.</p>
		<div class="welcome-composer">
			{@render composerForm()}
		</div>
	</div>
{:else}
	<div class="timeline-wrap">
		<div class="timeline-scroll" bind:this={scrollEl} onscroll={handleTimelineScroll}>
			{#each appState.turns as turn, i (i)}
				<ChatTurnView {turn} index={i} />
			{/each}
			{#if !appState.busyOnCurrentThread && appState.suggestions.length > 0}
				<div class="suggestions">
					{#each appState.suggestions as suggestion}
						<button class="suggestion-chip" onclick={() => appState.send(suggestion)}>
							{suggestion}
						</button>
					{/each}
				</div>
			{/if}
		</div>
		{#if !pinnedToBottom}
			<button
				class="jump-to-bottom"
				onclick={scrollToBottom}
				aria-label="Scroll to latest"
				title="Scroll to latest"
			>
				<ArrowDown size={16} />
				{#if appState.busyOnCurrentThread}<span class="jump-to-bottom-dot" aria-hidden="true"></span>{/if}
			</button>
		{/if}
	</div>
	{@render composerForm()}
{/if}

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		/* Directional shadow instead of a rule — the header floats a hair
		   above the timeline scrolling underneath it, same light-source
		   logic as the sidebar's own right-edge shadow. */
		box-shadow: 0 8px 16px -14px rgba(0, 0, 0, 0.5);
		background: color-mix(in srgb, var(--color-surface) 60%, transparent);
		/* Installed as a standalone PWA (apple-mobile-web-app-status-bar-style:
		   black-translucent), iOS draws the status bar over the page instead
		   of pushing content down like ordinary Safari does — without this,
		   the status bar's clock/battery area sits directly on top of the
		   sidebar toggle button, making it untappable. Falls back to the
		   plain 10px on browsers without safe-area support, same pattern as
		   the composer's safe-area-inset-bottom handling below. */
		padding: max(10px, env(safe-area-inset-top)) 16px 10px;
		gap: 12px;
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
		flex: 1;
	}

	/* Replaces the model selector, which moved into the composer's "+"
	   sheet — clamped to 2 lines since generated titles ("Debugging a
	   Go goroutine leak in the SearXNG client") routinely run past what
	   fits on one line at a readable size. */
	.thread-title {
		margin: 0;
		min-width: 0;
		font-family: var(--font-serif);
		font-size: 15px;
		font-weight: 600;
		line-height: 1.3;
		color: var(--color-text);
		display: -webkit-box;
		-webkit-line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
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
		margin-right: 2px;
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
		/* A first attempt here removed this entirely to stop mobile
		   browsers from auto-scrolling this container to bring a
		   newly-focused input into view — but that just traded one bug
		   for another: with nowhere to scroll, the keyboard shrinking
		   available height clipped the heading/composer instead of
		   scrolling them, a squashed/"crunched" look. The real fix for
		   the page-jumping was locking body itself (see app.css) — once
		   the *page* can't scroll, a local scrollable container here is
		   exactly as safe as .timeline-scroll already is in the
		   conversation view, which never had this problem. */
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
		line-height: 1.5;
	}

	.welcome .subtitle.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		font-style: italic;
		font-size: 17px;
		letter-spacing: 0.01em;
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

	/* Wraps .timeline-scroll so .jump-to-bottom can be positioned relative
	   to the scrolling viewport, not the whole page. */
	.timeline-wrap {
		position: relative;
		flex: 1;
		min-height: 0;
		display: flex;
	}

	.timeline-scroll {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		padding: 28px 20px;
		display: flex;
		flex-direction: column;
		gap: 24px;
	}

	/* Appears once the user scrolls away from the bottom (see
	   pinnedToBottom) — same "jump to latest" affordance Claude/ChatGPT
	   show while a reply is streaming, so scrolling up to read doesn't
	   mean losing your place once you're ready to catch up. The pulsing
	   dot only shows while a turn is actually in flight — otherwise this
	   is just "you're not at the bottom", not "something new is arriving". */
	.jump-to-bottom {
		position: absolute;
		bottom: 16px;
		left: 50%;
		transform: translateX(-50%);
		display: flex;
		align-items: center;
		justify-content: center;
		width: 36px;
		height: 36px;
		border-radius: 999px;
		border: none;
		background: var(--color-surface-2);
		color: var(--color-text);
		box-shadow: 0 4px 16px color-mix(in srgb, black 20%, transparent), var(--shadow-glass-edge);
		transition:
			background-color 0.15s var(--ease-out-expo),
			transform 0.15s var(--ease-out-expo);
	}

	.jump-to-bottom:hover {
		background: var(--color-surface-3);
		transform: translateX(-50%) translateY(-1px);
	}

	.jump-to-bottom-dot {
		position: absolute;
		top: -2px;
		right: -2px;
		width: 9px;
		height: 9px;
		border-radius: 999px;
		background: var(--color-accent);
		animation: jump-to-bottom-pulse 1.4s ease-in-out infinite;
	}

	@keyframes jump-to-bottom-pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.4;
		}
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
		border: none;
		background: var(--color-surface-2);
		color: var(--color-text-dim);
		border-radius: 999px;
		padding: 7px 14px;
		font-size: 12.5px;
		font-family: var(--font-sans);
		text-align: left;
		box-shadow: var(--shadow-xs);
		transition:
			color 0.15s var(--ease-out-expo),
			background-color 0.15s var(--ease-out-expo),
			transform 0.15s var(--ease-out-expo),
			box-shadow 0.15s var(--ease-out-expo);
	}

	.suggestion-chip:hover {
		color: var(--color-text);
		background: var(--color-surface-3);
		transform: translateY(-1px);
		box-shadow: var(--shadow-sm);
	}

	/* Column now, not a single row — the textarea sits on its own line
	   with room to breathe, the model/focus/attach controls that used to
	   crowd it live in .composer-toolbar underneath instead (see
	   ComposerMenu's "+" sheet, which absorbed the old inline model
	   selector — a row of 4-5 small controls doesn't survive phone
	   width, one thumb-reachable entry point does). */
	.composer {
		display: flex;
		flex-direction: column;
		gap: 8px;
		box-shadow: 0 -8px 16px -14px rgba(0, 0, 0, 0.5);
		padding: 16px;
		/* Clears iOS Safari's bottom toolbar / home-indicator area — falls
		   back to the plain 12px on browsers without safe-area support. */
		padding-bottom: max(12px, env(safe-area-inset-bottom));
	}

	.composer-toolbar {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.toolbar-spacer {
		flex: 1;
	}

	.attachment-chip {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		align-self: flex-start;
		max-width: 100%;
		border: none;
		background: var(--color-surface-2);
		border-radius: 999px;
		padding: 5px 6px 5px 10px;
		font-size: 12.5px;
		color: var(--color-text-dim);
		box-shadow: var(--shadow-xs);
	}

	.attachment-chip .attachment-name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.attachment-chip button {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
		border-radius: 999px;
		flex-shrink: 0;
		color: var(--color-text-dim);
	}

	.attachment-chip button:hover {
		background: var(--color-surface-3);
		color: var(--color-text);
	}

	.textarea-wrap {
		position: relative;
		display: flex;
	}

	.fake-placeholder {
		position: absolute;
		inset: 0;
		/* Not display: flex — a flex container treats a whitespace-only
		   text node between two inline elements as display: none (per the
		   flexbox spec's anonymous-item handling), which silently ate the
		   space between "Ask" and the "Polaris" span. Padding alone
		   already centers a single line of text the same height as the
		   textarea's own single row, so flex's vertical centering was
		   never actually needed here. */
		padding: 14px 16px;
		font-size: 16px;
		line-height: 1.5;
		font-family: var(--font-sans);
		color: var(--color-text-dim);
		pointer-events: none;
		white-space: nowrap;
		/* Horizontal-only: still truncates on narrow screens the same as
		   before. Vertical clipping is what was cutting the wordmark span
		   below down to a thin sliver — see .fake-placeholder .wordmark. */
		overflow-x: hidden;
		overflow-y: visible;
	}

	.fake-placeholder .wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		/* Asimovian's glyph metrics run taller than Lexend's at the same
		   font-size — inherited from the 1.5 line-height above, this
		   overflowed the placeholder's fixed-height box and, combined with
		   overflow: hidden, rendered as a squashed sliver instead of full
		   letterforms. A tighter line-height here (this span only — the
		   welcome heading's much larger wordmark instance never needed
		   this, it already has plenty of room) keeps it within the box
		   without needing the vertical clip that caused this at all. */
		line-height: 1.2;
	}

	textarea {
		width: 100%;
		resize: none;
		/* Carved-in well at rest instead of a flat hairline box — the accent
		   border only appears on focus (below), so idle the composer reads
		   as a soft trough in the surface, not a form field. */
		border: 1px solid transparent;
		background: var(--color-surface-2);
		box-shadow: var(--shadow-well);
		border-radius: var(--radius-lg);
		padding: 14px 16px;
		/* 16px, not 14 — anything smaller makes iOS Safari zoom the whole
		   page in on focus (it does this for any input/textarea under
		   16px), which is what was pushing the send button out of the
		   viewport. autoResize (see the action import above) handles
		   height, growing with content up to its maxHeight before
		   scrolling — same shape as Claude's composer, instead of a
		   fixed single row that just scrolls its own content out of view. */
		font-size: 16px;
		line-height: 1.5;
		font-family: var(--font-sans);
		color: var(--color-text);
		outline: none;
		/* A taller resting height than the bare single-row minimum — the
		   composer now carries its own toolbar underneath instead of
		   cramming everything onto one line, so it can afford to feel
		   like a real writing surface instead of a thin search bar. */
		min-height: 56px;
		max-height: 200px;
		overflow-y: auto;
		transition:
			border-color 0.15s var(--ease-out-expo),
			background-color 0.15s var(--ease-out-expo),
			box-shadow 0.15s var(--ease-out-expo);
	}

	textarea:hover {
		background: var(--color-surface-3);
	}

	textarea:focus {
		border-color: var(--color-accent);
		background: var(--color-surface);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 16%, transparent);
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

	:global(.spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
