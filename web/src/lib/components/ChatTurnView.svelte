<script lang="ts">
	import type { ChatTurn, FocusMode } from '$lib/types';
	import { appState } from '$lib/state.svelte';
	import ToolEvent from './ToolEvent.svelte';
	import RecommendationsCarousel from './RecommendationsCarousel.svelte';
	import ImageGallery from './ImageGallery.svelte';
	import HighlightCarousel from './HighlightCarousel.svelte';
	import ChartCard from './ChartCard.svelte';
	import AskUserQuestionCard from './AskUserQuestionCard.svelte';
	import WaveformAudioPlayer from './WaveformAudioPlayer.svelte';
	import { marked } from '$lib/markdown';
	import { renderMermaidIn } from '$lib/mermaid';
	import DOMPurify from 'dompurify';
	import {
		Pencil,
		RotateCcw,
		Check,
		X,
		Volume2,
		Loader2,
		Copy,
		Link2,
		Info,
		Orbit,
		Sunrise,
		Binoculars
	} from '@lucide/svelte';
	import { copyToClipboard } from '$lib/clipboard';
	import { autoResize } from '$lib/actions/autoResize';
	import { renderInlineCitations, sourceHostname as hostname } from '$lib/citations';
import { CHECK_DISPLAY, buildOracleNote, escapeHtml, focusSwitch } from '$lib/oracleLabels';
	import Asterism from './Asterism.svelte';
	import OracleConstellation from './OracleConstellation.svelte';
	import FieldIcon from './FieldIcon.svelte';
	import TurnInfoSheet from './TurnInfoSheet.svelte';
	import AttachmentChips from './AttachmentChips.svelte';
	import NetworkErrorBanner from './NetworkErrorBanner.svelte';
	import SourcesList from './SourcesList.svelte';
	import VariantSwitcher from './VariantSwitcher.svelte';
	import OfferLines from './OfferLines.svelte';
	import { pulsarState } from '$lib/pulsar.svelte';
	import { goto } from '$app/navigation';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';

	// noResearch: the composer's current Research toggle (inverted) — passed
	// through to AskUserQuestionCard so answering a pending question
	// preserves chat mode instead of always re-enabling research for that
	// reply. See ChatView.svelte's ChatTurnView invocation and
	// AskUserQuestionCard's answer()/enableWebSearch() split.
	let { turn, index, noResearch }: { turn: ChatTurn; index: number; noResearch: boolean } = $props();

	// Editing/regenerating always replaces starting at the preceding user
	// message's position (see gateway/turn.go's ForkThread call) — so an
	// assistant reply's variant group, if it has one, is keyed one index
	// back from its own. appState.variants has no entry at all for a
	// position that's never been touched, which is exactly what keeps the
	// switcher hidden on an ordinary, never-edited reply.
	let variantGroup = $derived(turn.role === 'assistant' ? appState.variants[index - 1] : undefined);
	let variantPosition = $derived(variantGroup ? variantGroup.ids.indexOf(variantGroup.active) : -1);

	function browseVariant(delta: number) {
		if (!variantGroup) return;
		const next = variantPosition + delta;
		if (next < 0 || next >= variantGroup.ids.length) return;
		void appState.swapVariant(variantGroup.ids[next]);
	}

	// Cards partition by Kind rather than preserving call-order
	// interleaving — a turn that produced both a recommendation call and
	// an image_search call renders one RecommendationsCarousel block and
	// one ImageGallery block, whichever are actually present. See
	// registry.go's Card.Kind doc comment.
	let mediaCards = $derived((turn.cards ?? []).filter((c) => c.kind !== 'image' && c.kind !== 'highlight'));
	let imageCards = $derived((turn.cards ?? []).filter((c) => c.kind === 'image'));
	let highlightCards = $derived((turn.cards ?? []).filter((c) => c.kind === 'highlight'));

	// highlight's cards live on turn.cards (a cumulative, turn-wide
	// snapshot set once at 'done'), not on the individual TimelineItem that
	// triggered the call — unlike show, which stashes its url/caption
	// directly on its own tool-call item. So "render inline where the call
	// happened" means picking one position in the timeline to anchor the
	// single merged carousel to, rather than branching per-item like show's
	// ToolEvent.svelte treatment: the last highlight call in the turn, so a
	// second call's cards don't get shown a second time at the first call's
	// earlier position.
	let lastHighlightTimelineIndex = $derived(
		(turn.timeline ?? []).reduce(
			(last, it, i) => (it.kind === 'tool' && it.tool === 'highlight' ? i : last),
			-1
		)
	);

	// Content can originate from fetched web pages (via web_read) as well
	// as the model itself, so sanitize before injecting as HTML — treat
	// it the same as any other untrusted input. renderInlineCitations runs
	// AFTER sanitize, turning the model's inline [Title](URL) links into
	// named source chips (e.g. "The Hollywood Reporter") for exactly the
	// claims that actually cite one of this turn's tracked sources — each
	// chip keeps its real href and opens the source directly, so there's
	// no detour through the source list below to find out what a bare
	// number pointed at.
	let renderedHtml = $derived(
		renderInlineCitations(
			DOMPurify.sanitize(marked.parse(turn.content || '') as string),
			turn.citations ?? [],
			turn.verification
		)
	);

	// Runs after renderedHtml (re)paints proseEl's DOM. Gated on
	// !turn.streaming: while a reply is still streaming, a ```mermaid fence
	// is briefly unclosed, and marked renders an unclosed fence as a full
	// code block the instant it opens — parsing that half-diagram live
	// would flash a render-failure note that vanishes once the fence
	// actually closes. Retry/regenerate and variant switching all replace
	// turn.content wholesale, so this just re-runs on the fresh DOM with no
	// manual cleanup of the old pass's output needed.
	//
	// Also re-tracks appState.settings.theme, not just renderedHtml — a
	// real bug found live: SettingsState.load() sets data-theme
	// asynchronously after mount (it's an /api/settings fetch), while a
	// loaded thread's content is already there on first render. Without
	// this dependency, a turn's mermaid blocks render once against
	// whatever data-theme happened to be set at that first paint (theme
	// defaults to 'dark' until the fetch resolves) and never get a second
	// chance — a light-theme user reopening an old thread saw dark-themed
	// diagrams stuck in their light UI permanently. Reading the theme here
	// makes the effect re-fire (and mermaid.ts re-render) once the real
	// preference lands, and again on any later in-session theme toggle.
	let proseEl = $state<HTMLElement>();
	$effect(() => {
		void renderedHtml;
		void appState.settings.theme;
		if (proseEl && !turn.streaming) void renderMermaidIn(proseEl);
	});

	let editing = $state(false);
	let editValue = $state('');

	function startEdit() {
		editValue = turn.content;
		editing = true;
	}

	function cancelEdit() {
		editing = false;
	}

	function saveEdit() {
		editing = false;
		appState.editMessage(index, editValue);
	}

	function onEditKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			saveEdit();
		} else if (e.key === 'Escape') {
			cancelEdit();
		}
	}

	function formatDuration(ms: number): string {
		const seconds = ms / 1000;
		if (seconds < 1) return `${Math.round(ms)}ms`;
		if (seconds < 60) return `${seconds.toFixed(1)}s`;
		const minutes = Math.floor(seconds / 60);
		return `${minutes}m ${Math.round(seconds % 60)}s`;
	}

	// Brief per-button checkmark confirmation after a successful copy —
	// local, unlike updateState in settings.svelte.ts, since there's
	// nothing to preserve across a remount: this turn's copy buttons
	// don't need to "still show progress" if you navigate away mid-copy,
	// the clipboard write is already synchronous and done.
	let copied = $state<'answer' | 'withSources' | null>(null);

	function flashCopied(which: 'answer' | 'withSources') {
		copied = which;
		setTimeout(() => {
			if (copied === which) copied = null;
		}, 1500);
	}

	async function copyAnswer() {
		try {
			await copyToClipboard(turn.content);
			flashCopied('answer');
			appState.showToast('Copied answer');
		} catch (err) {
			appState.showToast('Copy failed — clipboard access was blocked');
		}
	}

	// The nearest earlier assistant turn's own applied focus mode — see
	// oracleLabels.ts's buildOracleNote doc comment on why this (not just
	// oracleResult.focus_mode) is what lets the note tell "kept your X"
	// from "Switched X -> Y" from a plain first-time "answered as X".
	// Undefined turns (no oracleResult at all, or predating this field)
	// are skipped rather than treated as "no mode", since either would
	// otherwise read as a false "switch" the moment Oracle mode/this field
	// is turned on partway through an existing thread's history.
	let previousAppliedFocusMode = $derived.by(() => {
		for (let i = index - 1; i >= 0; i--) {
			const t = appState.turns[i];
			if (t?.role === 'assistant' && t.appliedFocusMode !== undefined) return t.appliedFocusMode;
		}
		return undefined;
	});

	let oracleNote = $derived(
		buildOracleNote(turn.oracleResult, turn.oracleFocusModeSource, turn.appliedFocusMode, previousAppliedFocusMode)
	);

	// F1 (mockups/oracle-mode.html): a mid-thread switch's note is a
	// tap-to-undo control, not just another way to open the info sheet —
	// "undo" means re-running this exact turn forced back to the mode it
	// switched away from, via the same rerun-as-X path TurnInfoSheet's own
	// Focus card button uses.
	let switchInfo = $derived(focusSwitch(turn.oracleFocusModeSource, turn.appliedFocusMode, previousAppliedFocusMode));

	function onOracleNoteClick() {
		if (switchInfo) {
			appState.retry(index, switchInfo.from as FocusMode);
		} else {
			infoSheetOpen = true;
		}
	}

	// The live "reading" choreography (OracleConstellation.svelte) — shown
	// only while this turn is still streaming and Oracle is actually
	// enabled, cut short once real output arrives (the "nothing has
	// streamed yet" window ComposerMenu's ring still falls back to when no
	// early 'oracle' event ever lands). Ghost threads run Oracle only when
	// the separate oracleGhostEnabled setting is on (see gateway/turn.go's
	// gate); when it isn't, the animation would be pure theater there —
	// skipped for the same reason the backend skips the real classification
	// call. appState.oracleWillRun encodes exactly that gate, including the
	// in-flight-turn ghost state (isGhostThread only flips once the turn is
	// done, so it alone would still animate the first ghost turn).
	//
	// checkCount is a fixed, approximate star count (one per CHECK_DISPLAY
	// entry, so a new prompts.yaml check adds a star as soon as it has a
	// display row), not this turn's real fired count — that isn't known until the turn
	// actually finishes, which is exactly what this animation is playing
	// *before*. Same simplification the mockup's own demo makes
	// (`buildConstellation`'s default `starCount = 6`); the margin note
	// that follows is what conveys the real, per-turn result.
	// constellationFolded flips when OracleConstellation finishes folding
	// (naturally or cut short), unmounting it: left mounted for the whole
	// streaming turn it sat above the answer as an invisible 34px gap, then
	// swapped for the note in a layout jump when the turn finished.
	let constellationFolded = $state(false);
	let showConstellation = $derived(
		appState.oracleWillRun && turn.streaming && !constellationFolded
	);
	let constellationCutShort = $derived(!!turn.timeline?.length || !!turn.content);

	let infoSheetOpen = $state(false);

	// Offer lines (docs/plans/oracle-mode.md's 7a). Pulsar/Daily navigate to
	// their own page, Safari sends the next turn, and "field" files this
	// thread under the Field Oracle matched — the chip carries the field's
	// id (Chip.field_id) since a Field's name isn't unique.
	const OFFER_META: Record<string, { icon: typeof Orbit | typeof FieldIcon; verb: string; label: (l?: string) => string }> = {
		pulsar: { icon: Orbit, verb: 'Set up', label: () => 'Check weekly as a <b>Pulsar</b>' },
		daily: { icon: Sunrise, verb: 'Add', label: () => 'Follow this in <b>Daily</b>' },
		safari: { icon: Binoculars, verb: 'Explore', label: () => 'Go deeper as a <b>Safari</b>' },
		field: { icon: FieldIcon, verb: 'Move', label: (l) => `Move to <b>${l ? escapeHtml(l) : 'a Field'}</b>` }
	};

	// A field chip is stale the moment the thread is in a Field — whether
	// this very chip just moved it, the composer picker did, or a later turn
	// on an older reply re-renders it — so it's filtered against the live
	// thread instead of only being dropped once, on click.
	//
	// Pulse ('pulsar') and Daily ('pulsar-daily') threads get no chips
	// (issue #146). The server now withholds these before persisting,
	// so this only hides chips already stored on older turns.
	let offers = $derived(
		(turn.oracleResult?.chips ?? []).flatMap((c) => {
			const meta = OFFER_META[c.key];
			if (!meta) return [];
			const source = appState.currentThread?.source;
			if (source === 'pulsar' || source === 'pulsar-daily') return [];
			if (c.key === 'field' && (!c.field_id || appState.activeFieldId)) return [];
			return [{ key: c.key, label: c.label, fieldId: c.field_id, meta }];
		})
	);

	// The preceding user turn's own content — what a Pulsar routine/Daily
	// block should actually check periodically, not the assistant's
	// answer to it. Falls back to this turn's own content on the rare
	// chance there's no preceding user turn (shouldn't happen for a real
	// assistant turn, but a synthetic/replayed one is cheap to guard).
	let seedText = $derived(appState.turns[index - 1]?.content ?? turn.content);

	// Which offer line is mid-flight, if any — the Pulsar chip now does a
	// round trip before navigating (see activateOffer), so it needs a
	// visible "working" state instead of looking like the tap did nothing.
	let offerBusy = $state<string | null>(null);

	// Safari is the one offer that stays in the chat: it sends the next turn
	// itself instead of navigating. The message names the style outright so
	// it works even when Oracle is off, and the pick goes out as a *manual*
	// focus mode — Oracle's own Safari bar is deliberately high (a Safari
	// takes over the thread), and a tap on this chip is exactly the explicit
	// request that bar is waiting for.
	const SAFARI_PROMPT =
		'I want this broken down in more depth, as an interactive, step-by-step exploration in the Safari style.';

	async function activateOffer(key: string, fieldId?: string, fieldName?: string) {
		if (offerBusy) return;
		if (key === 'field') {
			if (!fieldId) return;
			offerBusy = key;
			const err = await appState.moveCurrentThreadToField(fieldId);
			offerBusy = null;
			// The chip itself disappears on success (see offers' activeFieldId
			// filter), so the toast is the only confirmation there is.
			appState.showToast(err ? `Couldn't move it: ${err}` : `Moved to ${fieldName ?? 'that Field'}`);
			return;
		}
		if (key === 'safari') {
			appState.send(
				SAFARI_PROMPT,
				undefined,
				'safari',
				undefined,
				undefined,
				undefined,
				undefined,
				undefined,
				undefined,
				undefined,
				undefined,
				true
			);
			return;
		}
		if (key !== 'pulsar' && key !== 'daily') return;
		// The preceding message is only the right seed when the conversation
		// *is* the recurring question. On a follow-up ("what about the second
		// one?") it produces a routine or Daily block that returns nothing
		// when it runs, since a scheduled run has no thread to refer back to
		// — a real gap found live for Pulsar, and the same one for Daily
		// (issue #126). So derive standalone text from the whole conversation
		// first, and fall back to the raw message if that call can't produce
		// one.
		let text = seedText;
		let name: string | undefined;
		if (appState.currentThreadId) {
			offerBusy = key;
			const suggestion = await pulsarState.suggestSeed(key, appState.currentThreadId);
			offerBusy = null;
			if (suggestion) {
				text = suggestion.prompt;
				name = suggestion.name || undefined;
			}
		}
		pulsarState.pendingSeed = { kind: key, text, name };
		void goto(key === 'pulsar' ? '/pulsar' : '/daily');
	}

	async function copyAnswerWithSources() {
		const sources = (turn.citations ?? [])
			.map((c, i) => `${i + 1}. ${c.title || hostname(c.url)} — ${c.url}`)
			.join('\n');
		const text = sources ? `${turn.content}\n\nSources:\n${sources}` : turn.content;
		try {
			await copyToClipboard(text);
			flashCopied('withSources');
			appState.showToast('Copied answer with sources');
		} catch (err) {
			appState.showToast('Copy failed — clipboard access was blocked');
		}
	}
</script>

{#if turn.role === 'user'}
	<div class="row row-user" in:fly={{ y: 10, duration: 260, easing: quintOut }}>
		{#if turn.attachments?.length && !editing}
			<AttachmentChips attachments={turn.attachments} threadId={appState.currentThreadId} />
		{/if}
		<div class="user-block" class:editing>
			{#if editing}
				<div class="edit-box">
					<textarea
						bind:value={editValue}
						onkeydown={onEditKeydown}
						rows="2"
						use:autoResize={{ value: editValue, maxHeight: 320 }}
					></textarea>
					<div class="edit-actions">
						<button class="icon-btn" onclick={cancelEdit} title="Cancel"><X size={14} /></button>
						<button class="icon-btn" onclick={saveEdit} title="Save and re-run"><Check size={14} /></button>
					</div>
				</div>
			{:else}
				<div class="bubble bubble-user">{turn.content}</div>
				<button
					class="icon-btn edit-trigger"
					onclick={startEdit}
					disabled={turn.id === undefined || appState.busy}
					title="Edit and re-run"
				>
					<Pencil size={13} />
				</button>
			{/if}
		</div>
	</div>
{:else}
	<div class="row row-assistant" in:fly={{ y: 10, duration: 260, easing: quintOut }}>
		<div class="bubble bubble-assistant">
			{#if showConstellation}
				<div class="stage">
					<OracleConstellation
						checkCount={CHECK_DISPLAY.length}
						cutShort={constellationCutShort}
						onDone={() => (constellationFolded = true)}
					/>
				</div>
			{:else if oracleNote}
				<!-- B2: sits above tool calls/prose, same position the
					 OracleConstellation "reading" animation above folds away
					 from once a live turn resolves. F1: a mid-thread switch is
					 tap-to-undo (see onOracleNoteClick) with a one-time glow on
					 mount instead of the usual "open the info sheet" tap. -->
				<button
					class="oracle-note"
					class:glow={!!switchInfo}
					type="button"
					onclick={onOracleNoteClick}
					title={switchInfo ? 'Tap to undo — answer again as before' : 'Turn info'}
				>
					<Asterism size={13} class="o-icon" />
					{@html oracleNote}
				</button>
			{/if}
			{#if turn.timeline?.length}
				<div class="timeline">
					{#each turn.timeline as item, i (i)}
						{#if item.kind === 'tool' && item.tool === 'highlight'}
							<!-- No raw tool-call chip for highlight — like show, it
								 goes straight to the rendered cards, in place. -->
							{#if i === lastHighlightTimelineIndex && highlightCards.length}
								<HighlightCarousel cards={highlightCards} />
							{/if}
						{:else}
							<ToolEvent {item} />
						{/if}
					{/each}
				</div>
			{/if}

			{#if turn.errorKind === 'network'}
				<NetworkErrorBanner disabled={appState.busy} onRetry={() => appState.retry(index)} />
			{:else if turn.content}
				<div class="prose" bind:this={proseEl}>{@html renderedHtml}</div>
			{:else if turn.streaming}
				<div class="pending">…</div>
			{/if}

			{#if turn.streaming && turn.costUsd}
				<!-- The real turn-footer below is entirely hidden while
				     streaming (copy/read-aloud/variant-switch don't make
				     sense on an answer that isn't done) — this is just the
				     live running total from 'cost_update' events, reusing
				     the same footer/cost styling so it doesn't look like a
				     new element once the real footer takes over at "done". -->
				<div class="turn-footer">
					<span class="turn-cost">${turn.costUsd.toFixed(5)}</span>
				</div>
			{/if}

			{#if turn.pendingQuestion}
				<!-- The turn right after one that ended with a pending question
				     is, definitionally, however it was answered — see
				     tools/registry.go's PendingQuestion doc comment: "answering
				     it is just sending the next ordinary chat message". Passing
				     that content down lets the card show a resolved, static view
				     (which option was picked) instead of vanishing once it's no
				     longer the live, interactive one. -->
				<AskUserQuestionCard
					{turn}
					{noResearch}
					isLast={index === appState.turns.length - 1}
					answeredWith={appState.turns[index + 1]?.role === 'user' ? appState.turns[index + 1].content : undefined}
				/>
			{/if}

			{#if mediaCards.length}
				<RecommendationsCarousel cards={mediaCards} />
			{/if}

			{#if imageCards.length}
				<ImageGallery cards={imageCards} />
			{/if}

			{#if turn.chart}
				<ChartCard chart={turn.chart} />
			{/if}

			{#if turn.citations?.length}
				<SourcesList citations={turn.citations} />
			{/if}

			{#if !turn.streaming && turn.errorKind !== 'network'}
				<!-- Skipped for a network-error turn — copy/read-aloud/cost/
				     duration are all meaningless for a turn with no actual
				     answer, and the banner above already has its own Retry. -->
				<div class="turn-footer">
					{#if variantGroup && variantGroup.ids.length > 1}
					<VariantSwitcher position={variantPosition} total={variantGroup.ids.length} onBrowse={browseVariant} />
					{/if}
					<!-- Cost moved into TurnInfoSheet's own three-tier breakdown
						 (docs/plans/oracle-mode.md) — the footer now keeps only
						 duration and the action icons, same as the mockup's 4a
						 frame. Still shown live while streaming further up
						 (turn.streaming && turn.costUsd), a different "still
						 running" ticker this decision doesn't touch. -->
					{#if turn.durationMs !== undefined}
						<span class="turn-duration">{formatDuration(turn.durationMs)}</span>
					{/if}
					<button class="icon-btn" onclick={copyAnswer} title="Copy answer">
						{#if copied === 'answer'}
							<Check size={13} />
						{:else}
							<Copy size={13} />
						{/if}
					</button>
					{#if turn.citations?.length}
						<button class="icon-btn" onclick={copyAnswerWithSources} title="Copy answer with sources">
							{#if copied === 'withSources'}
								<Check size={13} />
							{:else}
								<Link2 size={13} />
							{/if}
						</button>
					{/if}
					{#if !turn.ttsAudioFile}
						<!-- Hidden once a persisted audio file exists — that's what
							 WaveformAudioPlayer below plays/scrubs; re-showing this
							 button then would just offer to re-spend TTS cost
							 synthesizing the same answer a second time. -->
						<button
							class="icon-btn"
							onclick={() => appState.readAloud(index)}
							title={appState.audio.speakingIndex === index ? 'Loading…' : 'Read aloud'}
						>
							{#if appState.audio.speakingIndex === index}
								<Loader2 size={13} class="spin" />
							{:else}
								<Volume2 size={13} />
							{/if}
						</button>
					{/if}
					<button
						class="icon-btn retry-btn"
						onclick={() => appState.retry(index)}
						disabled={appState.busy}
						title="Retry this turn"
					>
						<RotateCcw size={13} />
					</button>
					<!-- Always shown, Oracle on or off — the answer-stats section
						 of TurnInfoSheet (model/TTFT/tokens/cost) is meaningful
						 regardless; Oracle's own sections just add to it when it ran. -->
					<button class="icon-btn" onclick={() => (infoSheetOpen = true)} title="Turn info">
						<Info size={13} />
					</button>
				</div>
			{/if}
			{#if turn.ttsAudioFile}
				<WaveformAudioPlayer src={turn.ttsAudioFile} autoplay={appState.audio.justFinishedIndex === index} />
			{/if}
			{#if !turn.streaming && offers.length}
				<!-- 7a: offer lines below the footer — see OFFER_META's doc comment. -->
					<OfferLines {offers} busy={offerBusy} onActivate={activateOffer} />
			{/if}
			{#if infoSheetOpen}
				<TurnInfoSheet {turn} {index} onClose={() => (infoSheetOpen = false)} />
			{/if}
		</div>
	</div>
{/if}

<style>
	.row {
		display: flex;
	}

	.row-user {
		flex-direction: column;
		align-items: flex-end;
		gap: var(--space-sm);
	}

	.row-assistant {
		justify-content: flex-start;
	}

	.user-block {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		max-width: 640px;
	}

	/* Editing needs real room to type in, not the width of whatever short
	   bubble it's replacing — a one-line "what's the capital of france"
	   would otherwise hand the edit box a cramped ~250px, the opposite of
	   Claude.ai's full-width editor. Widens to the same max-width the main
	   composer uses instead of shrink-wrapping to the original message. */
	.user-block.editing {
		width: 100%;
		max-width: 640px;
		align-items: flex-start;
	}

	.bubble {
		font-size: 14px;
		line-height: 1.5;
	}

	.bubble-user {
		background: var(--color-surface-2);
		border: none;
		border-radius: var(--radius-lg);
		/* A real lift instead of a hairline — this is the one bubble shape
		   in the timeline, so it can afford to read as a small floating
		   card rather than a bordered box. Padding grown a notch too; the
		   original 10/14 read tight enough to feel like a form field. */
		box-shadow: var(--shadow-sm);
		padding: var(--space-md) var(--space-lg);
		color: var(--color-text);
		white-space: pre-wrap;
		word-break: break-word;
	}

	.bubble-assistant {
		width: 100%;
		max-width: 680px;
		font-size: 15px;
		line-height: 1.65;
	}

	.edit-trigger {
		opacity: 0;
		flex-shrink: 0;
	}

	.user-block:hover .edit-trigger {
		opacity: 1;
	}

	.edit-box {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		width: 100%;
	}

	.edit-box textarea {
		resize: vertical;
		border: 1px solid var(--color-accent-2);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: var(--space-md) var(--space-md);
		/* 16px, matching the main composer — anything smaller triggers
		   iOS Safari's zoom-on-focus. autoResize (see the action import
		   above) grows this with content instead of squeezing multi-line
		   text into a fixed 2-row box. */
		font-size: 16px;
		line-height: 1.5;
		font-family: inherit;
		color: var(--color-text);
		outline: none;
		min-height: 60px;
		max-height: 320px;
		overflow-y: auto;
	}

	.edit-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-xs);
	}

	.timeline {
		margin-bottom: var(--space-sm);
	}

	/* Holds OracleConstellation's live "reading" animation — fixed height
	   matches the component's own SVG so nothing shifts when it mounts. */
	.stage {
		position: relative;
		height: 34px;
		margin-bottom: var(--space-sm);
	}

	/* Oracle mode's margin note (docs/plans/oracle-mode.md's B2) — a
	   button, not static text: tapping it opens TurnInfoSheet, same
	   "the reply is the surface" idea as the ⓘ button in the footer below.
	   Ported from mockups/oracle-mode.html's .oracle-note. */
	.oracle-note {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		margin-bottom: var(--space-sm);
		min-height: 20px;
		border: none;
		background: none;
		padding: 0;
		text-align: left;
		font-size: 12.5px;
		color: var(--color-text-dim);
		cursor: pointer;
	}

	.oracle-note :global(.o-icon) {
		flex-shrink: 0;
		color: var(--color-accent);
		opacity: 0.85;
	}

	/* :global since the note's own bold/strikethrough spans arrive via
	   {@html} (buildOracleNote in oracleLabels.ts), not Svelte-templated
	   markup — same reasoning as .prose :global(...) below. The strings
	   themselves are built entirely from a fixed, developer-authored label
	   set (FOCUS_MODES, high-stakes/intent labels), never model or user
	   text, so this is safe without a DOMPurify pass. */
	.oracle-note :global(b) {
		color: var(--color-text);
		font-weight: 500;
	}

	.oracle-note :global(b.old) {
		text-decoration: line-through;
		color: var(--color-text-dim);
		font-weight: 400;
	}

	/* F1's one-time glow on a mid-thread switch note — plays once on
	   mount (no `infinite`), same idea as mockups/oracle-mode.html's
	   Web Animations version, just as a plain CSS animation. */
	.oracle-note.glow {
		animation: oracle-note-glow 1.4s ease-out;
	}

	.oracle-note.glow :global(.o-icon) {
		animation: oracle-note-icon-glow 0.9s cubic-bezier(0.16, 1, 0.3, 1);
	}

	@keyframes oracle-note-glow {
		0% {
			text-shadow: 0 0 0 transparent;
		}
		30% {
			text-shadow: 0 0 12px var(--color-accent);
		}
		100% {
			text-shadow: 0 0 0 transparent;
		}
	}

	@keyframes oracle-note-icon-glow {
		0% {
			transform: scale(1) rotate(0deg);
		}
		40% {
			transform: scale(1.5) rotate(20deg);
		}
		100% {
			transform: scale(1) rotate(0deg);
		}
	}

	/* A static "…" reads as stalled, not working — a slow, low-amplitude
	   breathing fade (not a spinner; nothing here should look busy or
	   mechanical) is enough to signal "still here" during the gap before
	   the first token lands. */
	.pending {
		color: var(--color-text-dim);
		animation: pending-breathe 1.6s ease-in-out infinite;
	}

	@keyframes pending-breathe {
		0%, 100% {
			opacity: 0.4;
		}
		50% {
			opacity: 1;
		}
	}

	/* Named inline citation chips — the model's [Title](URL) links land
	   here already sanitized, and renderInlineCitations (see citations.ts)
	   swaps in a real site name for a subset of them. Sits low and quiet
	   in the text flow (dim, small, no underline) so it reads as
	   attribution rather than a normal hyperlink; opens the source
	   directly on tap since the label already says what it is — no more
	   detour through the source list below to decode a bare number.
	   :global since these live inside {@html}-injected content, not
	   Svelte-templated markup. */
	.prose :global(.citation-chip) {
		display: inline-flex;
		align-items: center;
		max-width: 180px;
		margin: 0 0 0 var(--space-xs);
		padding: var(--space-xs) var(--space-sm);
		border: none;
		border-radius: var(--radius-full);
		background: var(--color-surface-2);
		color: var(--color-text-dim);
		font-size: 11.5px;
		font-weight: 500;
		text-decoration: none;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		vertical-align: middle;
		transform: translateY(-1px);
		cursor: pointer;
		box-shadow: var(--shadow-xs);
		transition: background-color 0.15s var(--ease-out-expo), color 0.15s var(--ease-out-expo), box-shadow 0.15s var(--ease-out-expo);
	}

	.prose :global(.citation-chip:hover) {
		background: var(--color-surface-3);
		color: var(--color-text);
		box-shadow: var(--shadow-sm);
	}

	/* The inline chip's own "found in source" mark — see citations.ts's
	   renderInlineCitations. --color-accent-2 reuses the app's existing
	   "citation chrome / informational" hue (already .source-index's
	   color below) rather than introducing a new semantic color. Sized to
	   sit inside the chip's 11.5px text without changing its height. */
	.prose :global(.citation-chip .citation-verified-icon) {
		width: 11px;
		height: 11px;
		flex-shrink: 0;
		margin-right: 3px;
		color: var(--color-accent-2);
	}

	.turn-footer {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		margin-top: var(--space-sm);
	}

	.turn-cost,
	.turn-duration {
		font-size: 11px;
		color: var(--color-text-dim);
		margin-right: var(--space-xs);
		font-variant-numeric: tabular-nums;
	}

	.prose :global(p) {
		margin: 0 0 var(--space-md) 0;
	}

	.prose :global(p:last-child) {
		margin-bottom: 0;
	}

	/* Weight contrast is the whole game here — serif headings at 700
	   against Lexend body copy at 400 creates real hierarchy without
	   raising the body-text floor. Tighter tracking on the biggest
	   heading; H3 stays sans + uppercase-caps feel via letter-spacing
	   so three levels of hierarchy actually feel distinct. */
	.prose :global(h1),
	.prose :global(h2) {
		font-family: var(--font-serif);
		font-weight: 700;
		line-height: 1.2;
		letter-spacing: -0.01em;
		margin: var(--space-xl) 0 var(--space-sm);
		color: var(--color-text);
	}

	.prose :global(h3) {
		font-family: var(--font-serif);
		font-weight: 700;
		line-height: 1.3;
		margin: var(--space-lg) 0 var(--space-sm);
		color: var(--color-text);
	}

	.prose :global(h1) {
		font-size: 24px;
		letter-spacing: -0.015em;
	}

	.prose :global(h2) {
		font-size: 20px;
	}

	.prose :global(h3) {
		font-size: 16px;
	}

	.prose :global(h1:first-child),
	.prose :global(h2:first-child),
	.prose :global(h3:first-child) {
		margin-top: 0;
	}

	.prose :global(ul),
	.prose :global(ol) {
		margin: 0 0 var(--space-md) 0;
		padding-left: var(--space-xl);
	}

	.prose :global(li) {
		margin-bottom: var(--space-xs);
	}

	.prose :global(blockquote) {
		margin: var(--space-md) 0;
		padding: var(--space-xs) 0 var(--space-xs) var(--space-lg);
		border-left: 2px solid var(--color-border-strong);
		color: var(--color-text-dim);
		font-style: italic;
	}

	.prose :global(pre) {
		background: var(--color-surface-2);
		border: none;
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-well);
		padding: var(--space-md) var(--space-md);
		overflow-x: auto;
		font-family: var(--font-mono);
		font-size: 13px;
		line-height: 1.5;
	}

	.prose :global(pre code) {
		background: transparent;
		padding: 0;
		font-size: inherit;
	}

	.prose :global(code) {
		background: var(--color-surface-2);
		border: none;
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-well);
		padding: var(--space-xs) var(--space-xs);
		font-family: var(--font-mono);
		font-size: 13px;
	}

	/* GitHub's table-scroll trick: display:block on the <table> itself (not
	   a wrapper div) turns it into a scrollable block box while its
	   tbody/tr/td children still get anonymous table boxes from the
	   browser, so the grid layout is untouched — width:max-content lets it
	   size to its natural content width, capped by max-width so overflow-x
	   kicks in instead of the table squeezing columns or blowing past the
	   bubble edge. */
	.prose :global(table) {
		display: block;
		width: max-content;
		max-width: 100%;
		overflow-x: auto;
		border-collapse: collapse;
		margin: 0 0 var(--space-md) 0;
		font-size: 13.5px;
	}

	.prose :global(th),
	.prose :global(td) {
		border: 1px solid var(--color-border);
		padding: var(--space-sm) var(--space-md);
		text-align: left;
		vertical-align: top;
	}

	.prose :global(th) {
		background: var(--color-surface-2);
		font-weight: 600;
		white-space: nowrap;
	}

	/* mermaid's own rendered <svg> is inserted into a plain wrapper div by
	   mermaid.ts, not a <pre> — max-width keeps a wide flowchart from
	   blowing past the bubble edge instead of forcing horizontal scroll on
	   the whole prose column, and overflow-x lets it scroll internally if
	   it still can't fit. position: relative anchors the toolbar below. */
	.prose :global(.mermaid-diagram) {
		position: relative;
		margin: 0 0 var(--space-md) 0;
		overflow-x: auto;
	}

	.prose :global(.mermaid-render svg) {
		max-width: 100%;
		height: auto;
	}

	/* The whole render pane (not just the SVG) is the tap target
	   mermaid.ts's lightbox opens from — cursor: zoom-in is the standard
	   "this image gets bigger" affordance, the same signal a browser's
	   own <img> gets at native resolution over a smaller display size. */
	.prose :global(.mermaid-render) {
		cursor: zoom-in;
	}

	/* Hidden until hover/focus, same "reveal on intent" language as
	   .edit-trigger above — a diagram is meant to be looked at, not
	   cluttered with chrome by default. :focus-within (not just :hover)
	   keeps the buttons reachable by keyboard: tabbing to one shouldn't
	   require a mouse hovering the diagram at the same time. */
	.prose :global(.mermaid-toolbar) {
		position: absolute;
		top: var(--space-sm);
		right: var(--space-sm);
		display: flex;
		gap: var(--space-xs);
		opacity: 0;
		transition: opacity 0.15s var(--ease-out-expo);
	}

	.prose :global(.mermaid-diagram:hover .mermaid-toolbar),
	.prose :global(.mermaid-diagram:focus-within .mermaid-toolbar) {
		opacity: 1;
	}

	/* icon-btn (app.css) assumes a plain surface behind it and stays
	   transparent until hover — sitting on top of a diagram's arbitrary
	   colors needs its own backdrop to stay legible at all times the
	   toolbar is visible, not just on hover. */
	.prose :global(.mermaid-btn) {
		background: var(--color-surface-2);
		box-shadow: var(--shadow-xs);
	}

	.prose :global(.mermaid-btn[aria-pressed='true']) {
		color: var(--color-accent-2);
	}

	/* The raw-source view mermaid.ts toggles in — a plain <pre> inside
	   .prose already picks up the generic `.prose :global(pre)` styling
	   above (background, mono font, well shadow); only the diagram
	   wrapper's own bottom margin is needed here, not a second one. */
	.prose :global(.mermaid-source-view) {
		margin: 0;
	}

	/* Left in place on the original code-block <pre> when mermaid.render()
	   throws — a syntax error degrades to exactly today's plain-code-block
	   behavior, plus this note explaining why it isn't a diagram. */
	.prose :global(.mermaid-error-note) {
		margin-top: var(--space-sm);
		font-size: 11.5px;
		font-style: italic;
		color: var(--color-text-dim);
	}

	.prose :global(a) {
		color: var(--color-accent-2);
	}

	.prose :global(a:hover) {
		color: var(--color-accent-2-strong);
	}

	:global(.spin) {
		animation: spin 1s linear infinite;
	}

	:global(.chevron) {
		transition: transform 0.15s ease;
	}

	:global(.chevron.open) {
		transform: rotate(90deg);
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
