<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import ChatTurnView from '$lib/components/ChatTurnView.svelte';
	import Transponder from '$lib/components/Transponder.svelte';
	import ChatComposer from '$lib/components/ChatComposer.svelte';
	import ChatHeader from '$lib/components/ChatHeader.svelte';
	import WelcomeScreen from '$lib/components/WelcomeScreen.svelte';
	import ThreadLoading from '$lib/components/ThreadLoading.svelte';
	import JumpToBottomButton from '$lib/components/JumpToBottomButton.svelte';
	import {
		Loader2,
		TriangleAlert,
		RotateCcw,
	} from '@lucide/svelte';
	import { uploadAttachment } from '$lib/upload';
	import { fieldsState } from '$lib/fields.svelte';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';
	import type { ChatTurn, FocusMode } from '$lib/types';

	let input = $state('');
	// Set by VoiceButton when a recording is transcribed via the Whisper
	// upload path (never for iOS's live Web Speech path, which is free) —
	// carried here rather than sent immediately so it rides along with
	// whatever text actually ends up submitted, same as attachedFile.
	let voiceCostUsd = $state<number | undefined>(undefined);
	let scrollEl: HTMLDivElement | undefined = $state();
	let showTransponder = $state(false);

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

	// Composer-only state — focusMode/deepResearch/research ride along in
	// every send() call already; attachedFiles gets uploaded (see submit
	// below) only at send time, not the instant it's picked, so backing
	// out of a message with files still attached never orphans an upload
	// nobody ends up sending. research starts true (on by default —
	// see ComposerMenu's doc comment on the prop); like focusMode/
	// deepResearch below it's now part of a thread's persisted sticky
	// config too (see docs/plans/pulsar-routines.md's "Prerequisite"
	// section and store.Thread.NoResearch) — it used to be composer-local
	// only, which meant leaving a thread in chat mode and reopening it
	// silently reset back to research-on.
	let focusMode = $state<FocusMode>('off');
	// True only from the moment the operator taps a focus mode in
	// ComposerMenu's Focus picker (see its bind:focusModeManual) until that
	// pick has been delivered — submit() clears it right after capturing it
	// for the send, and the thread-switch effect below clears it too.
	// "Manual" is scoped to a live composer pick "for this specific
	// message" (see gateway/protocol.go's ClientMessage.FocusModeSource doc
	// comment), not to whatever a thread's sticky config happens to already
	// equal — which is why the reset-after-send matters: leaving it true
	// kept every later message in the session flagged manual, so Oracle's
	// focus check could never switch and the composer badge stopped
	// following Oracle's picks. Threaded through send() as a one-shot flag
	// so Oracle's focus check stays free to run on every send that isn't a
	// real, just-made manual override.
	let focusModeManual = $state(false);
	let deepResearch = $state(false);
	let research = $state(true);
	// Ghost mode (issue #67) — unlike focusMode/deepResearch/research, this
	// has no persisted thread config to round-trip (a ghost thread writes
	// nothing to store.Store at all — see gateway/protocol.go's Anonymous
	// doc comment), so it's reset on every thread switch/new-thread instead
	// of restored from one (see the thread-switch $effect below). New-
	// thread-only, locked by ComposerMenu itself the moment the session has
	// any messages.
	let ghostMode = $state(false);
	let attachedFiles = $state<File[]>([]);
	let uploading = $state(false);

	// Applies the Settings panel's standing default focus mode exactly
	// once, the moment it's actually loaded (settings.load() is async,
	// fired from +layout.svelte's onMount — this component can easily
	// render before it resolves). Guarded so it never overwrites a
	// manual choice made from the composer's "+" menu afterward; "off"
	// is itself a valid loaded value, which is why this checks
	// settings.loaded rather than the value of defaultFocusMode itself.
	// The field this thread belongs to, or is about to be created in —
	// drives the header pill and the welcome line (see AppState.activeFieldId).
	let activeField = $derived(fieldsState.byId(appState.activeFieldId));

	let focusModeInitialized = false;
	$effect(() => {
		if (appState.settings.loaded && !focusModeInitialized) {
			focusMode = appState.settings.defaultFocusMode;
			focusModeManual = false;
			focusModeInitialized = true;
		}
	});

	// Applies a thread's own sticky config (appState.threadFocusMode/
	// threadDeepResearch/threadNoResearch, populated by openThread()) — or
	// the standing Settings default for a new thread — on every real,
	// user-initiated thread switch.
	//
	// Keyed on appState.threadConfigEpoch (bumped only by openThread()/
	// newThread()) rather than currentThreadId. Keying on the id was a real
	// bug found live: the 'done' handler also sets currentThreadId (a
	// brand-new thread learning its own id from its first answer), so this
	// effect fired right after that answer finished and reapplied whatever
	// threadFocusMode still held from the *previous* thread — silently
	// snapping the composer back to the last thread's mode even though
	// Oracle had just applied a different one for this turn. The epoch
	// moves only on an actual switch, so a turn completing never triggers
	// it.
	//
	// Also applies for the null (newThread()) case now, instead of leaving
	// it to the settings-default effect above: that one runs exactly once
	// (guarded by focusModeInitialized), so "start a new thread" was
	// falling back to nothing and carrying the previous thread's focus
	// mode/research toggles straight over. Reset here to the settings
	// default (focus) and the ordinary defaults (research on, deep research
	// off).
	let lastConfigEpoch = -1;
	$effect(() => {
		const epoch = appState.threadConfigEpoch;
		if (epoch === lastConfigEpoch) return;
		lastConfigEpoch = epoch;
		if (appState.currentThreadId === null) {
			// A field's own default focus mode wins over the global standing
			// default when set ('' inherits — not "force off", which is the
			// real value 'off'). Same seeding point as the global default, so
			// a manual pick afterward is still untouched (focusModeManual).
			const fieldFocus = fieldsState.byId(appState.pendingFieldId)?.default_focus_mode;
			focusMode = fieldFocus ? fieldFocus : appState.settings.defaultFocusMode;
			focusModeManual = false;
			deepResearch = false;
			research = true;
		} else {
			focusMode = appState.threadFocusMode;
			focusModeManual = false;
			deepResearch = appState.threadDeepResearch;
			research = !appState.threadNoResearch;
		}
		// Ghost mode has no persisted config to restore, unlike the three
		// above — always reset on any thread switch (including to/from
		// "new thread"), never carried over. A stale "on" from a
		// just-finished ghost session silently making the next, unrelated
		// conversation a ghost too would be a bigger surprise than just
		// needing to flip it on again each time.
		ghostMode = false;
	});

	// Oracle mode's own focus pick (docs/plans/oracle-mode.md) only ever
	// showed up in the reply's own margin note/info sheet, easy to miss
	// entirely — a real gap found live: the composer gave no visible sign
	// Oracle had just switched into Shopper for that turn, even though it
	// genuinely had (confirmed against the DB: applied_focus_mode/
	// oracle_focus_mode_source were both set correctly; the frontend
	// display was the actual gap). Mirrors a manual pick's own visible
	// effect — the trigger's badge — the instant Oracle actually resolves,
	// same "as if the operator had picked it" idea (threads.focus_mode is
	// already updated server-side to match — see
	// gateway/turn.go's second SetThreadConfig call — so this doesn't
	// diverge from what's actually sticky).
	//
	// Not gated on !last.streaming any more: Oracle's verdict now arrives
	// on its own early 'oracle' event (see its doc comment), which lands
	// seconds before the answer finishes — the whole point of keying here
	// is to show the badge at that real moment rather than after 'done'.
	// focusModeManual stays false: this is Oracle's own pick, not an
	// operator override, so Oracle stays free to change it again next turn.
	//
	// Guarded by the turn object itself, not its index. An index guard was
	// a real bug: newThread() empties appState.turns, so the next thread's
	// first answer lands back at index 1 — exactly where a previous
	// thread's single-exchange turn already left the guard — and Oracle's
	// pick for that new thread was then silently skipped. The turn object
	// is a fresh proxy per thread, so identity can't collide that way. The
	// guard still does its other job: the early 'oracle' event sets it, and
	// 'done' re-setting the same fields finds it already applied.
	let lastOracleFocusAppliedTurn: ChatTurn | null = null;
	$effect(() => {
		const last = appState.turns[appState.turns.length - 1];
		if (
			last?.role === 'assistant' &&
			last.oracleFocusModeSource === 'oracle' &&
			last !== lastOracleFocusAppliedTurn
		) {
			lastOracleFocusAppliedTurn = last;
			// Fall back to 'off', not to "leave it alone": Oracle clears a
			// mode it set earlier (appliedFocusMode "" / undefined,
			// focus_cleared) when no mode fits the new message, and the
			// composer badge has to reflect that too — previously this
			// branch only ran for a non-empty pick, so a cleared mode stayed
			// showing on the trigger.
			focusMode = (last.appliedFocusMode || 'off') as FocusMode;
			focusModeManual = false;
		}
	});

	function handleAttach(files: File[]) {
		attachedFiles = [...attachedFiles, ...files];
	}

	function removeAttachment(index: number) {
		attachedFiles = attachedFiles.filter((_, i) => i !== index);
	}

	// Clipboard File objects from an image copy (screenshot tools, "Copy
	// image" from a browser, etc.) commonly arrive with an empty .name —
	// the attachment chip below renders each file's .name, so an unnamed
	// blob would show as a blank pill. Giving it a synthetic name keeps
	// the chip legible without needing any UI just for the paste path.
	const extensionForImageType: Record<string, string> = {
		'image/png': 'png',
		'image/jpeg': 'jpg',
		'image/gif': 'gif',
		'image/webp': 'webp',
		'image/svg+xml': 'svg'
	};

	// Reuses the exact same attach → upload-on-send pipeline as the "+"
	// menu's file input (see handleAttach/submit above and ComposerMenu's
	// handleFileChange) — a paste is just another way to arrive at the
	// same attachedFiles state, so nothing downstream needs to know which
	// path produced it. Only image types are handled; a text/plain or
	// text/html paste falls through untouched so normal pasting still works.
	function onPaste(e: ClipboardEvent) {
		const items = e.clipboardData?.items;
		if (!items) return;
		for (const item of items) {
			if (item.kind !== 'file' || !item.type.startsWith('image/')) continue;
			const file = item.getAsFile();
			if (!file) continue;
			e.preventDefault();
			const named = file.name
				? file
				: new File([file], `pasted-image-${Date.now()}.${extensionForImageType[file.type] ?? 'png'}`, {
						type: file.type
					});
			handleAttach([named]);
			break;
		}
	}

	async function submit() {
		// Re-entrancy guard: the send button's own `disabled={uploading}`
		// only stops a second *click*, but onKeydown below calls submit()
		// straight from the textarea's Enter handler with no such check.
		// Without this, a fast second Enter fired while an earlier
		// attachment upload is still in flight races ahead of it —
		// appState.send() isn't called (and appState.busy isn't set) until
		// after the await below, so the second, upload-free submit() can
		// call send() and flip busy=true first; when the first call's
		// upload then finishes and it finally calls send(), the this.busy
		// check there silently drops it — losing the first message and its
		// attachment, while the accidental second message goes through
		// instead.
		if (uploading) return;

		const text = input;
		const files = attachedFiles;
		const sttCostUsd = voiceCostUsd;
		input = '';
		attachedFiles = [];
		voiceCostUsd = undefined;

		// "Manual" means "the operator picked this focus mode for THIS
		// message" (see gateway/protocol.go's ClientMessage.FocusModeSource),
		// not "this thread is now manually steered forever" — so it clears
		// the moment it's been delivered, while the picked mode itself stays
		// on screen (and sticky server-side via SetThreadConfig). Without
		// this, one tap on a focus mode marked every later message in the
		// session manual too, so Oracle's focus check could never switch
		// again and the composer badge stopped reflecting Oracle's picks —
		// the exact "it says academic but the chip still says shopper"
		// failure found live. A local copy is what gets sent so the values
		// captured below stay consistent even after the reset.
		const manualFocusForThisMessage = focusModeManual;
		focusModeManual = false;

		// The very first message of a new "Talk to Weaver" session (issue
		// #94) — no thread exists yet, so this must go through
		// startWeaverThread (which pins source: 'weaver') rather than the
		// ordinary send(). Every later message in the same thread just
		// uses send() normally below: the backend re-derives Weaver-ness
		// from the thread's own persisted source at that point (see
		// gateway/turn.go's isWeaverThread), not from anything the client
		// resends. Attachments never apply here — ComposerMenu (the only
		// way to attach a file) is hidden for a Weaver session.
		if (appState.startingWeaverThread && !appState.currentThreadId) {
			appState.startWeaverThread(text, appState.pendingWeaverModel);
			return;
		}

		if (files.length === 0) {
			appState.send(
				text,
				sttCostUsd,
				focusMode,
				deepResearch,
				undefined,
				!research,
				undefined,
				undefined,
				ghostMode,
				undefined,
				undefined,
				manualFocusForThisMessage
			);
			return;
		}

		uploading = true;
		// Falls back to whichever uploads actually succeeded (or none) on a
		// partial/total upload failure — an error toast would be nicer, but
		// silently dropping the whole message because one attachment failed
		// is worse than answering with the rest, or without any of them.
		const uploaded = (await Promise.all(files.map(uploadAttachment))).filter(
			(a) => a !== null
		);
		uploading = false;
		appState.send(
			text,
			sttCostUsd,
			focusMode,
			deepResearch,
			uploaded,
			!research,
			undefined,
			undefined,
			ghostMode,
			undefined,
			undefined,
			manualFocusForThisMessage
		);
	}

	// The active thread's title, shown in the header now that the model
	// selector has moved into the composer's "+" sheet — falls back to
	// nothing for a brand-new thread whose title hasn't loaded yet (or
	// hasn't been generated server-side).
	//
	// appState.currentThread, not a lookup in appState.threads (the
	// sidebar list) — that list excludes pulsar-sourced threads entirely
	// (see store.ListThreads' doc comment), so a pulse's own title/
	// favorite/pulsar_routine_id would all resolve to undefined here if
	// this still searched it.
	let currentThread = $derived(appState.currentThread);
	let currentThreadTitle = $derived(currentThread?.title ?? '');

	// A pulse's thread view gets a "back to routine" affordance instead of
	// the plain sidebar-toggle-only header, per docs/plans/pulsar-routines.md's
	// "Pulse detail" UI — routing there always via /pulsar/[id]'s own link,
	// which tags the URL with ?pulsar=<routineId> (see that route's
	// openPulse). Gated on the thread's own pulsar_routine_id too, not just
	// the query param, so this can't be spoofed into showing on an
	// unrelated thread by hand-editing the URL.
	let pulsarBackRoutineId = $derived(
		currentThread?.pulsar_routine_id != null ? page.url.searchParams.get('pulsar') : null
	);

	// isWeaverThread (issue #94, "Talk to Weaver") strips this view down to
	// just a plain timeline + textarea/send composer — no attachments/
	// focus-mode/deep-research menu, no Transponder call button, no voice
	// input, none of which apply to a tool-driven Weaver session. True
	// once a real thread's own persisted source says so (currentThread is
	// only populated after the first turn completes — see
	// refreshCurrentThreadIfMatches), OR while composing the very first
	// message on /constellation/weaver/new, before any thread exists yet
	// (see appState.startingWeaverThread's own doc comment).
	let isWeaverThread = $derived(currentThread?.source === 'weaver' || appState.startingWeaverThread);

	// A turn ending in a lone user message with nothing after it is never
	// a valid "finished" state — dispatch() always pushes the user turn
	// and its streaming assistant placeholder together, so the only way
	// the timeline ends on a bare user turn is a turn that never got a
	// reply: the connection dropped mid-generation, the tab closed, the
	// server restarted, or (the bug this was built for) navigating away
	// mid-stream orphaned it. Whatever the cause, showing nothing here
	// looks identical to a message vanishing into the void — this is the
	// signal to offer a real way out instead of the composer just quietly
	// sitting there idle.
	//
	// !appState.threadTurnInProgress rules out one more real case this
	// heuristic used to get wrong: a Pulsar pulse (docs/plans/pulsar-routines.md)
	// runs with no live WebSocket connection at all, so
	// busyOnCurrentThread — which only reflects a turn *this* client
	// itself started — is always false for one, even while it's
	// genuinely still running server-side. Without this check, opening
	// an in-progress pulse showed a false "didn't finish, retry?" banner
	// — actively dangerous, not just wrong, since Retry would fork/resend
	// while the original turn might still be writing to the same thread.
	let lastTurnInterrupted = $derived(
		appState.turns.length > 0 &&
			appState.turns[appState.turns.length - 1].role === 'user' &&
			!appState.busyOnCurrentThread &&
			!appState.threadTurnInProgress
	);

	function retryInterrupted() {
		appState.retry(appState.turns.length);
	}

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

	// A turn is streaming, but for some other thread — the composer here
	// still can't send (only one turn runs at a time per connection,
	// regardless of thread), but showing a "Stop" control that would
	// actually cancel a different, unrelated thread would be actively
	// wrong, not just unhelpful. See appState.busyOnCurrentThread.
	let busyElsewhere = $derived(appState.busy && !appState.busyOnCurrentThread);
</script>

{#snippet composerForm()}
	<ChatComposer
		bind:input
		bind:focusMode
		bind:focusModeManual
		bind:deepResearch
		bind:research
		bind:voiceCostUsd
		{ghostMode}
		{attachedFiles}
		{uploading}
		{isWeaverThread}
		{busyElsewhere}
		onSubmit={submit}
		{onKeydown}
		{onPaste}
		onAttach={handleAttach}
		onRemoveAttachment={removeAttachment}
		onCall={() => (showTransponder = true)}
	/>
{/snippet}

<svelte:head>
	<title>{pageTitle}</title>
</svelte:head>

<ChatHeader
	bind:ghostMode
	{pulsarBackRoutineId}
	{isWeaverThread}
	{currentThreadTitle}
	{currentThread}
	{activeField}
/>

{#if !showTransponder}
{#if appState.turns.length === 0 && appState.threadLoading}
	<!-- A thread the user just asked for is still on its way (see
	     AppState.threadLoading). Its own state, not the WelcomeScreen —
	     this is what keeps a slow open (a Pulsar pulse's events fetch can
	     run to a couple of MB) from reading as "the tap bounced me back to
	     the homescreen". No composer: sending doesn't make sense until the
	     thread's own sticky config has loaded. -->
	<ThreadLoading />
{:else if appState.turns.length === 0}
	<!-- Empty state: composer floats centered, like Claude/OpenWebUI's
	     landing view, instead of sitting pinned at the bottom of a mostly
	     empty screen. Switches to the normal scrolling-history layout the
	     instant the first message is sent. -->
		<WelcomeScreen {isWeaverThread} {activeField}>
			{@render composerForm()}
		</WelcomeScreen>
{:else}
	<div class="timeline-wrap">
		<div class="timeline-scroll" bind:this={scrollEl} onscroll={handleTimelineScroll}>
			{#each appState.turns as turn, i (i)}
				<!-- noResearch: the composer's current toggle, so answering a
				     pending question (AskUserQuestionCard) preserves chat mode
				     instead of silently re-enabling research — see that
				     component's answer()/enableWebSearch() split. -->
				<ChatTurnView {turn} index={i} noResearch={!research} />
			{/each}
			{#if appState.threadTurnInProgress && appState.turns[appState.turns.length - 1]?.role !== 'assistant'}
				<!-- A pulse (or any other turn with no live client attached)
				     genuinely still running server-side — see
				     threadTurnInProgress's doc comment. No retry action here:
				     unlike lastTurnInterrupted below, there's nothing to
				     retry, just a wait for the poll in openThread() to pick
				     up the finished answer.

				     Only shown before this turn has produced its first
				     persisted event: once it has, openThread() appends a
				     synthetic streaming assistant turn built from those
				     events (see its own doc comment) and that turn's own
				     "…"/timeline rendering already covers "still working" —
				     showing this banner too on top of it would be a
				     redundant second spinner for the exact same fact. -->
				<div class="in-progress" in:fly={{ y: 10, duration: 260, easing: quintOut }}>
					<Loader2 size={15} class="spin" />
					<span>Still running…</span>
				</div>
			{:else if lastTurnInterrupted}
				<div class="interrupted" in:fly={{ y: 10, duration: 260, easing: quintOut }}>
					<div class="interrupted-message">
						<TriangleAlert size={15} />
						<span>This response didn't finish — the connection dropped or the session was interrupted.</span>
					</div>
					<button class="btn btn-accent" onclick={retryInterrupted}>
						<RotateCcw size={15} />
						Retry
					</button>
				</div>
			{:else if !appState.busyOnCurrentThread && appState.suggestions.length > 0}
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
			<JumpToBottomButton busy={appState.busyOnCurrentThread} onClick={scrollToBottom} />
		{/if}
	</div>
	{@render composerForm()}
{/if}
{/if}

{#if showTransponder}
	<!-- The normal turn list (and everything else above) is unmounted
	     while a call is active, not just visually covered — it used to
	     stay fully mounted underneath the full-screen overlay, which meant
	     ChatTurnView's own WaveformAudioPlayer for the turn Transponder
	     just synthesized (autoplay={appState.audio.justFinishedIndex ===
	     index}) ALSO autoplayed the exact same file independently, a beat
	     apart from Transponder's own playback — two real, separate audio
	     engines racing the same clip. Live-described as "like a second
	     version playing on top with a slight delay," which is exactly what
	     that is: a genuine phase/comb-filter artifact from real double
	     playback, not a synthesis or mic-constraint quality issue (both
	     were red herrings chased first). -->
	<Transponder onClose={() => (showTransponder = false)} />
{/if}

<style>
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
		padding: var(--space-2xl) var(--space-xl);
		display: flex;
		flex-direction: column;
		gap: var(--space-xl);
	}

	/* A real recovery moment, not a quiet dead end — sized and weighted
	   like an actual interruption in the conversation (full-width card,
	   a real .btn-accent, same size as the send button) rather than a
	   small icon tucked into a footer. Warm/amber rather than the harsh
	   danger red — nothing failed destructively, the connection just
	   dropped; this should read as "pick up where you left off," not
	   "something broke." */
	.interrupted {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-lg);
		flex-wrap: wrap;
		max-width: 680px;
		background: var(--color-surface-2);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-sm);
		padding: var(--space-lg) var(--space-lg);
	}

	.interrupted-message {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		flex: 1;
		min-width: 220px;
		font-size: 13.5px;
		line-height: 1.4;
		color: var(--color-text-dim);
	}

	.interrupted-message :global(svg) {
		flex-shrink: 0;
		color: var(--color-accent);
	}

	.in-progress {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		max-width: 680px;
		background: var(--color-surface-2);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-sm);
		padding: var(--space-lg) var(--space-lg);
		font-size: 13.5px;
		color: var(--color-text-dim);
	}

	.in-progress :global(svg) {
		flex-shrink: 0;
		color: var(--color-accent);
	}

	/* Sits right below the last answer, inside the scrolling timeline —
	   not pinned near the composer, since these are about that specific
	   answer, not a persistent app-level control. */
	.suggestions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin-top: -6px;
	}

	.suggestion-chip {
		border: none;
		background: var(--color-surface-2);
		color: var(--color-text-dim);
		border-radius: var(--radius-full);
		padding: var(--space-sm) var(--space-lg);
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

	:global(.spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
