import type {
	ChatTurn,
	ModelOption,
	Thread,
	ServerEvent,
	Citation,
	Card,
	ChartSpec,
	PendingQuestion,
	StoredEvent,
	TimelineItem,
	FocusMode,
	UploadedAttachment,
	MessageAttachment,
	VariantGroup,
	MessageSearchResult,
	VerificationMark,
	OracleResult
} from './types';
import { AgentSocket } from './ws';
import { AudioPlayer } from './audio.svelte';
import { SettingsState } from './settings.svelte';
import { getUserLocation, requestFreshLocation } from './geolocation';
import { pulsarState } from './pulsar.svelte';
import { fieldsState } from './fields.svelte';

import { ThreadSearchState } from './threadSearch.svelte';
import { ToastState } from './toasts.svelte';
import { VersionState } from './versionCheck.svelte';
import { fetchEventsByTurn, buildTurnsFromMessages } from './threadTurns';
import { applyStreamingEvent, closeOpenReasoning } from './turnEvents';
import {
	safeParseJSON,
	safeParseObject,
	applyVerification,
	buildTimelineFromEvents,
	debugBeacon
} from './stateHelpers';

// Re-exported so existing `import { debugBeacon } from '$lib/state.svelte'` call
// sites keep working after the helpers moved to stateHelpers.ts.
export { debugBeacon };

// Exported (not just the singleton below) so tests can construct fresh,
// isolated instances instead of sharing the one live during a real
// session.
export class AppState {
	turns = $state<ChatTurn[]>([]);
	threads = $state<Thread[]>([]);

	// Sidebar's "search past chats" box — see ThreadSearchState.search.
	threadSearch = new ThreadSearchState();
	get threadSearchQuery() {
		return this.threadSearch.query;
	}
	get threadSearchResults() {
		return this.threadSearch.results;
	}
	get threadSearchLoading() {
		return this.threadSearch.loading;
	}
	models = $state<ModelOption[]>([]);
	selectedModel = $state<string>('');
	// threadFocusMode/threadDeepResearch/threadNoResearch are the
	// just-opened thread's sticky turn config, set by openThread() below —
	// ChatView.svelte's currentThreadId effect applies these to its own
	// composer-local focusMode/deepResearch/research state, since ChatView
	// (not AppState) still owns the actual live composer state.
	// selectedModel above needs no such relay: openThread() writes it
	// directly, since the model selector already reads straight from
	// AppState with no local copy in between.
	threadFocusMode = $state<FocusMode>('off');
	threadDeepResearch = $state(false);
	threadNoResearch = $state(false);
	// Bumped by openThread()/newThread() — the two places that are a real,
	// user-initiated thread switch — so ChatView's config-restoring effect
	// reacts to an actual switch instead of to currentThreadId changing for
	// ANY reason. That distinction is load-bearing: the 'done' handler also
	// sets currentThreadId (a brand-new thread learning its own id), and an
	// effect keyed on the id would fire there too, re-applying whatever
	// threadFocusMode still held from the *previous* thread — a real bug
	// found live, where Oracle would pick a new mode for a new thread's
	// first answer and the composer would silently snap back to the last
	// thread's mode the moment that turn finished.
	threadConfigEpoch = $state(0);
	// Whether the just-opened thread's turn is genuinely still running
	// server-side — set by openThread() below from GetThread's
	// turn_in_progress (see gateway's IsTurnInFlight). Exists specifically
	// for Pulsar: a pulse has no live WebSocket connection, so
	// busyOnCurrentThread (which only reflects turns *this* client
	// started) can't tell "still running" apart from "crashed" the way it
	// can for an ordinary chat turn. ChatView.svelte's lastTurnInterrupted
	// checks this before falling back to its old "didn't finish" guess.
	threadTurnInProgress = $state(false);
	// The currently-open thread's own row (title/favorite/pulsar_routine_id/
	// etc.), set directly from openThread()'s GetThread fetch rather than
	// looked up in `threads` below — that list excludes pulsar-sourced
	// threads entirely (see store.ListThreads' doc comment: a pulse is
	// only ever meant to be browsed via /pulsar, not the ordinary
	// sidebar), so ChatView.svelte's header title/back-button/favorite
	// toggle would all silently break on a pulse's own thread view if
	// they depended on finding it in that list. Also just more correct
	// in general even for a normal thread: this is always exactly what
	// GetThread returned for whatever's actually on screen, not a
	// separately-fetched list entry that could be a beat stale right
	// after a rename/favorite.
	currentThread = $state<Thread | null>(null);
	// pendingFieldId is the Field a NEW (not yet id'd) thread will be
	// created in — set by startThreadInField, sent as field_id on that
	// thread's first turn only (see dispatch()), and kept afterward so the
	// header's field pill still has something to read: for a thread created
	// this session currentThread stays null (see openThread's doc comment on
	// why), so activeFieldId falls back to this. Cleared by newThread()/
	// openThread(), the two real "switch to a different thread" actions —
	// after an openThread, currentThread.field_id is authoritative.
	pendingFieldId = $state<string | null>(null);
	// The field the thread on screen belongs to (or is about to be created
	// in) — what the header pill, the composer's field chip and
	// ThreadMenu's "Move to field" all read.
	activeFieldId = $derived(this.currentThread ? (this.currentThread.field_id ?? null) : this.pendingFieldId);
	currentThreadId = $state<string | null>(null);
	connected = $state(false);
	busy = $state(false);
	totalCost = $state(0);
	// Version polling lives in VersionState; these delegates keep the
	// appState.version / .deployment surface the settings panel and tests read.
	versionState = new VersionState(() => ({ busy: this.busy, currentThreadId: this.currentThreadId }));
	get version() {
		return this.versionState.version;
	}
	set version(v: string) {
		this.versionState.version = v;
	}
	get deployment() {
		return this.versionState.deployment;
	}
	set deployment(v: string) {
		this.versionState.deployment = v;
	}
	get versionCheckInterval() {
		return this.versionState.interval;
	}

	// contextTokens is the current thread's last-known prompt+completion
	// size, from the LLM's own usage numbers — settings.contextWindowTokens
	// (the auto-compaction threshold) is the denominator for the % shown
	// next to it in +page.svelte.
	contextTokens = $state(0);
	// Thread-level prompt-cache totals (issue #107) — summed input tokens
	// and how many of those were cache reads, all-time across the thread's
	// turns, shown as a hit % in ThreadMenu next to thread cost. Loaded with
	// the thread, then accumulated from each "done" event the same way
	// totalCost is.
	promptTokens = $state(0);
	cacheReadTokens = $state(0);

	// Follow-up suggestions for the most recent answer — persisted on the
	// last assistant message (see StoredMessage.suggestions), so openThread
	// restores them; cleared on new dispatch/new thread since there's no
	// "most recent answer" yet at that point.
	suggestions = $state<string[]>([]);

	// Which message positions in the current thread have more than one
	// reply (an edit or regenerate happened there) — keyed by index into
	// `turns`, same as GetThread's response (see gateway/threads.go's
	// buildVariantsMap). ChatTurnView reads this to decide whether to show
	// the "‹ 2/3 ›" switcher on a given assistant reply at all; a position
	// with no entry here has never been touched.
	variants = $state<Record<number, VariantGroup>>({});

	// Desktop: sidebar sits inline, open by default. Mobile: it's an
	// overlay drawer, closed by default so the chat is visible first.
	// +layout.svelte sets the initial value from viewport width on mount.
	sidebarOpen = $state(true);

	settings = new SettingsState();
	audio = new AudioPlayer();

	// Brief, app-level confirmation banners — see ToastState.
	toastState = new ToastState();
	get toasts() {
		return this.toastState.toasts;
	}

	showToast(message: string, durationMs = 2000) {
		this.toastState.show(message, durationMs);
	}

	// Identifies which thread + turn object an in-flight response belongs
	// to — distinct from currentThreadId/turns, which reflect what's
	// currently *displayed*. Navigating to a different thread mid-stream
	// doesn't cancel anything server-side (there's no cancellation), so
	// without this, stray token/tool events kept landing on whatever was
	// last in the newly-displayed array — including appending an
	// assistant's reply straight into a user bubble. pendingThreadId is
	// null until the first event reveals it, for a brand-new thread.
	private pendingTurn: ChatTurn | null = null;
	private pendingUserTurn: ChatTurn | null = null;
	private pendingThreadId: string | null = null;
	private pendingIsNewThread = false;
	// True while the in-flight turn belongs to a ghost session — see
	// dispatch()'s ghostMode handling and handleEvent's 'done'/
	// 'user_message' cases, which skip the sidebar/URL-visible side
	// effects a still-ghost thread shouldn't trigger (see isGhostThread's
	// doc comment for why the thread itself is otherwise a fully real,
	// server-persisted one now). $state rather than a plain field because
	// oracleWillRun below reads it reactively — a ghost turn must not
	// animate Oracle's pre-read the backend skipped.
	private pendingGhost = $state(false);
	// True for as long as the currently open thread is still tagged ghost
	// server-side (see store.go's ghost schema comment) — a ghost thread
	// is a fully real, persisted thread from its very first turn under
	// the full-fidelity redesign, just excluded from the sidebar/URL/
	// chat-search while unpromoted, and it withholds memory/chat_search
	// tool access for as long as this stays true. Set from the 'done'
	// handler once a turn resolves, cleared by newThread()/openThread()
	// (abandoning) and by a successful promote() (see below) — nothing
	// else needs to touch it, since the server independently re-derives
	// real ghost status per turn from its own DB row regardless of what
	// this flag says.
	isGhostThread = $state(false);

	// True when Oracle will actually read this turn's message — the same
	// gate gateway/turn.go applies (Oracle enabled, and not a ghost turn
	// unless the oracle_ghost_enabled opt-in is on). The Oracle UI keys off
	// this instead of appState.settings.oracleEnabled alone so a ghost turn
	// doesn't play the constellation/reading animation for a classification
	// the backend skipped. Reads pendingGhost as well as isGhostThread: a
	// brand-new ghost thread's first turn is already ghost server-side while
	// isGhostThread is still false (it only flips on 'done'), and the
	// composer's ghost toggle is composer-local state, so pendingGhost is
	// the only live ghost signal available during that turn.
	oracleWillRun = $derived(
		this.settings.oracleEnabled &&
			(!(this.pendingGhost || this.isGhostThread) || this.settings.oracleGhostEnabled)
	);

	// startingWeaverThread (issue #94, "Talk to Weaver") is true only for
	// the brief pre-send window on /constellation/weaver/new: currentThread
	// is still null (no thread exists yet, so its own .source can't be
	// checked), but ChatView.svelte still needs to know to render the
	// stripped Weaver composer instead of the normal one. Once the first
	// message actually sends and completes, currentThread.source ===
	// 'weaver' becomes the real, persisted source of truth (see
	// refreshCurrentThreadIfMatches) and this flag stops mattering — it's
	// reset by newThread() rather than by that transition, so leaving it
	// true a little longer than strictly necessary is harmless.
	startingWeaverThread = $state(false);
	// Set alongside startingWeaverThread by /constellation/weaver/new —
	// carries Constellation's configured model (constellationState.config
	// ?.model) over to ChatView.svelte's submit(), which calls
	// startWeaverThread(text, pendingWeaverModel) for the session's first
	// message. A plain string field rather than AppState importing
	// ConstellationState directly, for the same "keep AppState decoupled
	// from Constellation's own feature-specific state" reasoning
	// startWeaverThread's own doc comment gives.
	pendingWeaverModel = $state<string | undefined>(undefined);

	// Set when the user navigates away (openThread to a different thread,
	// or newThread()) while a turn is still in flight for pendingThreadId.
	// Only one turn can ever be in flight at a time from this client (busy
	// gates every send/retry/edit globally — see send() below), but which
	// thread that turn belongs to and which thread is on screen can
	// diverge the moment the user switches threads mid-stream. Without
	// this flag, the 'done' handler's stillWatching check couldn't tell
	// "still on the brand-new thread this turn is creating" (currentThreadId
	// still null because nothing rebound it) apart from "explicitly
	// backed out via newThread() while a DIFFERENT pending turn was still
	// running" (currentThreadId also null) — both looked identical, so the
	// abandoned turn's answer silently became "current" again once it
	// finished. Reset to false at the start of every new dispatch().
	private pendingAbandoned = false;

	// True only when the in-flight turn (if any) belongs to the thread
	// currently on screen — unlike `busy`, which is true whenever ANY turn
	// is in flight anywhere. `busy` is still what gates starting a second
	// turn (send/retry/editMessage below) since only one can run at a time
	// per connection regardless of which thread it's for; this getter is
	// purely about what the composer's Stop button is allowed to target,
	// so switching threads mid-stream doesn't leave a visible "Stop"
	// control that would actually cancel a different, unrelated thread.
	get busyOnCurrentThread(): boolean {
		if (!this.busy || this.pendingAbandoned) return false;
		// Mirrors handleEvent's stillWatching exactly (this getter is really
		// "would stillWatching be true if 'done' fired right now") —
		// currentThreadId === null covers watching a brand-new thread's own
		// creation, where pendingThreadId has already been learned (from
		// 'user_message') but currentThreadId is deliberately left unset
		// until 'done' actually adopts it. Comparing pendingThreadId to
		// currentThreadId directly here would wrongly read as "not busy on
		// this thread" for that whole window despite the user watching it
		// stream in real time.
		return this.currentThreadId === null || this.currentThreadId === this.pendingThreadId;
	}

	// Bumped at the start of every openThread() call; a call whose fetch
	// resolves after a newer one has already started discards its own
	// result instead of overwriting it — otherwise two rapid thread
	// switches (fast sidebar clicks, browser back/forward between two
	// /t/<id> URLs) could resolve out of order and leave the view/URL
	// pointed at whichever happened to respond second, not whichever was
	// clicked last.
	private openThreadSeq = 0;

	private socket: AgentSocket;

	constructor() {
		this.socket = new AgentSocket(
			(e) => this.handleEvent(e),
			(connected) => (this.connected = connected),
			() => this.resyncAfterReconnect()
		);
	}

	// Fires after the socket drops and reconnects. If a turn was in flight
	// when that happened, its events are gone — the backend finished the
	// work and persisted the result independently of whether anyone was
	// still listening, so the fix is to go re-fetch the thread from the
	// database rather than wait for a stream that's never coming.
	private async resyncAfterReconnect() {
		if (!this.busy) return;

		if (this.pendingAbandoned) {
			// The in-flight turn belongs to a thread the user has since
			// navigated away from. The server is still finishing it
			// independently regardless (see this class's other comments on
			// that) — but there's nothing to recover for whatever's actually
			// on screen right now, so just drop tracking instead of
			// re-fetching and yanking the view toward a thread nobody's
			// looking at.
			this.busy = false;
			this.pendingTurn = null;
			this.pendingUserTurn = null;
			this.pendingThreadId = null;
			this.pendingIsNewThread = false;
			this.pendingGhost = false;
			this.pendingAbandoned = false;
			return;
		}

		const threadId = this.pendingThreadId ?? this.currentThreadId;
		if (!threadId) {
			// Disconnected before the server even acknowledged the user
			// message (no thread id yet) — nothing to fetch. Surface it as
			// a retryable error rather than leaving the UI stuck mid-"…".
			if (this.pendingTurn) {
				this.pendingTurn.streaming = false;
				if (!this.pendingTurn.content) {
					this.pendingTurn.content = 'Connection was lost before this could be confirmed. Please retry.';
				}
			}
			this.busy = false;
			this.pendingTurn = null;
			this.pendingUserTurn = null;
			this.pendingThreadId = null;
			this.pendingIsNewThread = false;
			this.pendingGhost = false;
			return;
		}

		// A still-ghost thread's id 404s through openThread just like a
		// genuinely missing one (GetThread excludes it — see store.go's
		// ghost schema comment) — openThread's own 404 handling already
		// no-ops gracefully, so a dropped/reconnected socket mid-ghost-turn
		// loses that turn the same way a refresh would, consistent with
		// ghost mode's "discarded unless promoted" design (see
		// gateway/protocol.go's Anonymous doc comment).
		await this.openThread(threadId);
		this.busy = false;
		this.pendingTurn = null;
		this.pendingUserTurn = null;
		this.pendingThreadId = null;
		this.pendingIsNewThread = false;
		this.pendingGhost = false;
		void this.loadThreads();
	}

	connect() {
		this.socket.connect();
		void this.startVersionCheck();
	}

	startVersionCheck() {
		return this.versionState.start();
	}

	checkVersion() {
		return this.versionState.check();
	}

	toggleSidebar() {
		this.sidebarOpen = !this.sidebarOpen;
	}

	closeSidebar() {
		this.sidebarOpen = false;
	}

	// Manual per-message read-aloud, triggered from the speaker icon next
	// to a turn's retry button — delegates to AudioPlayer, which owns the
	// playback state; this just supplies the turn data it needs and takes
	// the resulting cost.
	async readAloud(assistantTurnIndex: number) {
		await this.audio.readAloud(this.turns, assistantTurnIndex, this.currentThreadId, (cost) => {
			this.totalCost += cost;
		});
	}

	async loadModels() {
		const res = await fetch('/api/models');
		this.models = (await res.json()) ?? [];
		const def = this.models.find((m) => m.default);
		this.selectedModel = def?.id ?? this.models[0]?.id ?? '';
	}

	async loadThreads() {
		const res = await fetch('/api/threads');
		this.threads = (await res.json()) ?? [];
	}

	searchThreads(query: string) {
		this.threadSearch.search(query);
	}

	clearThreadSearch() {
		this.threadSearch.clear();
	}


	async openThread(id: string) {
		const seq = ++this.openThreadSeq;

		// A turn is in flight for a thread other than the one we're about
		// to show — mark it abandoned so its eventual 'done' can't silently
		// resurrect it as current (see pendingAbandoned's doc comment).
		// Returning to the pending thread itself (or nothing being in
		// flight at all) un-abandons it, so navigating back before it
		// finishes still updates live, as expected.
		if (this.busy) {
			this.pendingAbandoned = id !== this.pendingThreadId;
		}

		let res: Response;
		let eventsByTurn: Map<string, StoredEvent[]>;
		try {
			[res, eventsByTurn] = await Promise.all([fetch(`/api/threads/${id}`), fetchEventsByTurn(id)]);
		} catch {
			// Network failure (e.g. the brief window right as a restart's
			// old process goes away and the new one isn't answering yet) —
			// same "don't leave this silent" reasoning as the !res.ok
			// branch below.
			if (seq === this.openThreadSeq) this.showToast("Couldn't load that thread — please try again");
			return;
		}
		if (seq !== this.openThreadSeq) return; // superseded by a newer openThread() call
		if (!res.ok) {
			// 404 means the id genuinely doesn't exist (deleted, a stale
			// bookmark, a hidden variant id) — nothing to show, staying
			// silent here is correct. Anything else (503 above all — see
			// handleGetThread's doc comment on why a transient DB hiccup
			// during the restart-overlap window now surfaces as 503, not a
			// misleading 404) used to no-op identically, leaving the view
			// stuck on whatever was on screen before with zero indication
			// anything went wrong — which is exactly what "I clicked a
			// thread and it didn't switch" looks like from the outside.
			if (res.status !== 404) this.showToast("Couldn't load that thread — please try again");
			return;
		}
		const data = await res.json();
		debugBeacon('currentThreadId set (openThread)', {
			from: this.currentThreadId,
			to: id,
			pendingThreadId: this.pendingThreadId,
			busy: this.busy
		});
		this.currentThreadId = id;
		this.currentThread = data as Thread;
		this.pendingFieldId = null;
		// A stale ghost session's flag must never leak into whichever
		// thread is opened next — dispatch()'s stickiness check (see
		// isGhostThread's doc comment) would otherwise treat this thread's
		// very next message as a ghost turn too. Harmless in practice here
		// (GetThread, which this fetch just succeeded against, already
		// excludes an unpromoted ghost thread's id — see store.go's ghost
		// schema comment — so id could never actually BE one), but reset
		// unconditionally anyway rather than relying on that as the only
		// guard. newThread() already resets this for "start fresh"; opening
		// an existing thread needs the same reset.
		this.isGhostThread = false;
		this.syncURL(id);
		this.totalCost = data.cost_usd ?? 0;
		this.contextTokens = data.context_tokens ?? 0;
		this.promptTokens = data.prompt_tokens ?? 0;
		this.cacheReadTokens = data.cache_read_tokens ?? 0;
		this.variants = data.variants ?? {};
		// Sticky turn config — see threadFocusMode's doc comment above.
		// data.model falls back to the current selection rather than ''
		// since every thread row has always had a real model value; this
		// guard only matters for a response shape this code doesn't
		// actually expect.
		if (data.model) this.selectedModel = data.model;
		this.threadFocusMode = (data.focus_mode || 'off') as FocusMode;
		this.threadDeepResearch = data.deep_research ?? false;
		this.threadNoResearch = data.no_research ?? false;
		// Announce the switch (see threadConfigEpoch's doc comment) only
		// once every config field above is in place, so ChatView's effect
		// reads a fully-populated set rather than a half-updated one.
		this.threadConfigEpoch++;
		this.threadTurnInProgress = data.turn_in_progress ?? false;
		// The server just flipped this pulse's seen flag (handleGetThread's
		// MarkPulseSeen) — refresh the sidebar/routine-row amber counts so
		// they don't sit stale until something else happens to reload them.
		if (data.source === 'pulsar') void pulsarState.loadUnreadCounts();
		const messages = data.messages ?? [];

		// Group persisted events by turn_id so each assistant message's
		// timeline (thinking steps, tool calls) can be reattached below —
		// otherwise reopening a thread shows only the bare final answer,
		// with everything that led up to it gone. Older messages predating
		// this feature have turn_id "" and simply get no timeline back.
		let turns = buildTurnsFromMessages(messages, eventsByTurn, this.currentThreadId);

		// A turn is still streaming for this exact thread — the user
		// navigated away mid-generation and came back. The fetch above only
		// has what's persisted (the user's question; the assistant reply
		// doesn't persist until the turn finishes), so without this the
		// reopened thread would show a permanently "…" placeholder even
		// after the real answer finishes server-side, since handleEvent
		// would keep mutating pendingTurn — an object no longer part of
		// whatever array openThread just replaced turns with. Splice the
		// live pair back in (replacing the fetch's last message, which is
		// that same in-flight user question) so it keeps updating live and
		// resolves normally once "done" arrives.
		if (id === this.pendingThreadId && this.pendingTurn) {
			turns = this.pendingUserTurn
				? [...turns.slice(0, -1), this.pendingUserTurn, this.pendingTurn]
				: [...turns, this.pendingTurn];
		} else if (this.threadTurnInProgress) {
			// Genuinely still running with nobody's own live socket attached
			// to it (a pulse, or this same client after a refresh/reconnect
			// — the branch above already covers "my own socket is still
			// watching this exact turn"). The trailing user message already
			// carries this turn's turn_id (AddMessage persists it before
			// agent.Run even starts — see gateway/turn.go), and
			// logTurnEvent has been persisting its thinking/tool-call
			// events all along, so eventsByTurn already has everything
			// produced so far; buildTurnsFromMessages just had nowhere to
			// attach it, since there's no assistant message row yet.
			// Without this, a refresh or reconnect mid-turn silently threw
			// away every tool call already run, leaving only a bare "Still
			// running…" with no detail (see ChatView.svelte's in-progress
			// banner) until the whole thing finally finished.
			const last = messages[messages.length - 1];
			if (last?.role === 'user' && last.turn_id && eventsByTurn.has(last.turn_id)) {
				turns = [
					...turns,
					{
						role: 'assistant',
						content: '',
						streaming: true,
						timeline: buildTimelineFromEvents(eventsByTurn.get(last.turn_id)!)
					}
				];
			}
		}
		this.turns = turns;

		// Suggestions are a "what's next" prompt for the last answer, so
		// only the most recent assistant message's set is relevant here.
		const lastAssistant = [...messages].reverse().find((m: any) => m.role === 'assistant');
		this.suggestions = lastAssistant ? safeParseJSON<string>(lastAssistant.suggestions) : [];
		this.closeSidebarIfMobile();

		// A pulse still running has no live WebSocket pushing its own
		// "done" — polling re-fetch is the only way this view ever learns
		// the turn actually finished, short of the user manually
		// reloading (see threadTurnInProgress's doc comment).
		this.pollWhileInProgress(id);
	}

	// pollTimer's id, if a poll is currently scheduled — cleared and
	// re-armed by pollWhileInProgress, so navigating away or a completed
	// turn never leaves a stray timer re-fetching a thread nobody's
	// looking at anymore.
	private pollTimer: ReturnType<typeof setTimeout> | null = null;

	private pollWhileInProgress(id: string) {
		if (this.pollTimer !== null) {
			clearTimeout(this.pollTimer);
			this.pollTimer = null;
		}
		if (!this.threadTurnInProgress) return;
		this.pollTimer = setTimeout(() => {
			// Stale by the time this fires (navigated to a different
			// thread, or away entirely) — openThread(id) would silently
			// clobber whatever's actually on screen now.
			if (this.currentThreadId !== id) return;
			void this.openThread(id);
		}, 4000);
	}

	// Browses to a different reply at some earlier edit/regenerate point —
	// see store.SetActiveVariant's doc comment for what this does
	// server-side. currentThreadId never changes: the swap endpoint
	// responds with the same shape GetThread does, just built from
	// whichever variant is now active, so this only ever updates what's
	// displayed, never which thread is open.
	async swapVariant(variantId: string) {
		if (!this.currentThreadId) return;
		const id = this.currentThreadId;

		// Sequential, not Promise.all — the events fetch resolves through
		// EffectiveThreadID server-side (see handleThreadEvents), which
		// only points at the new variant once this POST has actually
		// committed. Firing both at once let the GET occasionally win the
		// race and read the variant being switched away from, silently
		// dropping that reply's reasoning/tool-call timeline.
		const res = await fetch(`/api/threads/${id}/variant`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ variant_id: variantId })
		});
		if (!res.ok || id !== this.currentThreadId) return; // stale — navigated away mid-request
		const data = await res.json();
		const eventsByTurn = await fetchEventsByTurn(id);
		this.totalCost = data.cost_usd ?? 0;
		this.contextTokens = data.context_tokens ?? 0;
		this.promptTokens = data.prompt_tokens ?? 0;
		this.cacheReadTokens = data.cache_read_tokens ?? 0;
		this.variants = data.variants ?? {};
		const messages = data.messages ?? [];
		this.turns = buildTurnsFromMessages(messages, eventsByTurn, this.currentThreadId);
		const lastAssistant = [...messages].reverse().find((m: any) => m.role === 'assistant');
		this.suggestions = lastAssistant ? safeParseJSON<string>(lastAssistant.suggestions) : [];
	}

	// Re-fetches just the variants map for id — used after a live edit/
	// retry finishes (see handleEvent's 'done' case), where the turns
	// array is already correct from live streaming and only the variants
	// map (which ServerEvent never carries) can be stale.
	private async refreshVariants(id: string) {
		const res = await fetch(`/api/threads/${id}`);
		if (!res.ok || id !== this.currentThreadId) return; // stale — navigated away mid-request
		const data = await res.json();
		this.variants = data.variants ?? {};
	}

	// startThreadInField opens a blank composer scoped to a field — the
	// field detail view's "New thread" button and its omnibox both start
	// here. Seeds the field's default model when it names one this install
	// actually has (a model removed from config since would otherwise leave
	// the picker on nothing); the field's default focus mode is applied by
	// ChatView's config effect, which owns the live composer state. The
	// caller navigates to '/' afterward — see routes/fields/[id].
	startThreadInField(fieldId: string) {
		this.newThread();
		this.pendingFieldId = fieldId;
		const model = fieldsState.byId(fieldId)?.default_model;
		if (model && this.models.some((m) => m.id === model)) this.selectedModel = model;
	}

	// moveCurrentThreadToField files the open thread under a field (or
	// out of any, with null) and keeps every reader of activeFieldId in
	// step — currentThread for an opened thread, pendingFieldId for one
	// created this session. Returns the server's error text on failure.
	async moveCurrentThreadToField(fieldId: string | null): Promise<string | null> {
		const id = this.currentThreadId;
		if (!id) return 'No open thread';
		const res = await fieldsState.moveThread(id, fieldId);
		if (!res.ok) return res.error;
		this.pendingFieldId = fieldId;
		if (this.currentThread) {
			this.currentThread = { ...this.currentThread, field_id: fieldId ?? undefined };
		}
		void this.loadThreads();
		return null;
	}

	// setThreadField is the composer picker's entry point: files the open
	// thread under a field, or — for a brand-new thread with no id yet —
	// stages the field as pendingFieldId so the first message creates the
	// thread already inside it (the same path startThreadInField takes, minus
	// the navigation). Also seeds the field's default model on that staged
	// path, for the same reason startThreadInField does. null = out of any
	// field. Returns the server's error text on failure.
	async setThreadField(fieldId: string | null): Promise<string | null> {
		if (this.currentThreadId !== null) return this.moveCurrentThreadToField(fieldId);
		this.pendingFieldId = fieldId;
		const model = fieldId ? fieldsState.byId(fieldId)?.default_model : '';
		if (model && this.models.some((m) => m.id === model)) this.selectedModel = model;
		return null;
	}

	newThread() {
		// Same reasoning as openThread's abandonment check: navigating to
		// "no thread selected" can never match whatever the in-flight
		// turn's thread actually is (even a still-forming brand-new thread
		// whose id isn't known yet, i.e. pendingThreadId is itself still
		// null — restarting the new-thread flow explicitly abandons that
		// one too, not just an existing thread's turn).
		if (this.busy) this.pendingAbandoned = true;

		debugBeacon('currentThreadId set (newThread)', { from: this.currentThreadId, busy: this.busy });
		this.pendingFieldId = null;
		this.currentThreadId = null;
		this.currentThread = null;
		this.turns = [];
		this.totalCost = 0;
		this.contextTokens = 0;
		this.promptTokens = 0;
		this.cacheReadTokens = 0;
		this.suggestions = [];
		// A leftover ghost session's flag must never carry over into
		// whatever's opened next — see isGhostThread's doc comment.
		this.isGhostThread = false;
		this.startingWeaverThread = false;
		this.pendingWeaverModel = undefined;
		// Announce the switch — ChatView's effect resets the composer to the
		// standing Settings default here rather than letting the previously
		// open thread's focus mode/research toggles leak into the new one
		// (see threadConfigEpoch's doc comment).
		this.threadConfigEpoch++;
		this.syncURL(null);
		this.closeSidebarIfMobile();
	}

	// Keeps the address bar's /t/<id> in step with currentThreadId, so a
	// refresh, a backgrounded tab reloading, or just copying the URL lands
	// back on the same thread instead of the homescreen — previously
	// nothing about which thread was open lived in the URL at all.
	//
	// Deliberately the raw History API, not SvelteKit's goto()/pushState:
	// goto() performs a real client-side navigation, which would remount
	// routes/t/[id]/+page.svelte and re-run its openThread effect against
	// a thread that's already loaded (or, worse, mid-stream) here in
	// AppState — the one source of truth both routes render from (see
	// ChatView.svelte). replaceState only touches the address bar.
	private syncURL(threadId: string | null) {
		if (typeof window === 'undefined') return;
		const path = threadId ? `/t/${threadId}` : '/';
		if (window.location.pathname === path) return;
		window.history.replaceState(window.history.state, '', path);
	}

	// Picking a thread (or starting a new one) should dismiss the drawer
	// on mobile so the chat is immediately visible, but leave the sidebar
	// alone on desktop where it's pinned inline, not an overlay.
	private closeSidebarIfMobile() {
		if (typeof window !== 'undefined' && window.innerWidth < 768) {
			this.sidebarOpen = false;
		}
	}

	async deleteThread(id: string) {
		await fetch(`/api/threads/${id}`, { method: 'DELETE' });
		if (this.currentThreadId === id) this.newThread();
		await this.loadThreads();
	}

	// Manual rename from the sidebar — always wins over the one-time
	// LLM-generated title a new thread gets after its first turn,
	// whether the rename happens before or after that.
	async renameThread(id: string, title: string) {
		const trimmed = title.trim();
		if (!trimmed) return;
		await fetch(`/api/threads/${id}`, {
			method: 'PATCH',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ title: trimmed })
		});
		await this.loadThreads();
		await this.refreshCurrentThreadIfMatches(id);
	}

	// Re-titles using the whole thread (every message so far), not just
	// the opening question the automatic one-time title was generated
	// from — see gateway/turn.go's regenerateTitle. Returns whether it
	// succeeded so ThreadMenu can show an error instead of just closing
	// silently; a manual rename afterward still always wins over this,
	// same as it does over the automatic title.
	async regenerateTitle(id: string): Promise<boolean> {
		const resp = await fetch(`/api/threads/${id}/regenerate-title`, { method: 'POST' });
		if (!resp.ok) return false;
		await this.loadThreads();
		await this.refreshCurrentThreadIfMatches(id);
		return true;
	}

	// Re-fetches just id's own thread row into currentThread when it's the
	// one currently open — loadThreads() alone doesn't cover this for a
	// pulsar-sourced thread, since ListThreads excludes those entirely
	// (see its doc comment), so ChatView.svelte's header would otherwise
	// keep showing the pre-rename title/favorite state for a pulse
	// indefinitely. Cheap and harmless to call for a normal thread too —
	// GetThread is a single-row lookup, not a full page reload.
	private async refreshCurrentThreadIfMatches(id: string) {
		if (this.currentThreadId !== id) return;
		const res = await fetch(`/api/threads/${id}`);
		if (!res.ok || this.currentThreadId !== id) return; // stale — navigated away mid-request
		this.currentThread = (await res.json()) as Thread;
	}

	// Promotes the currently open ghost thread into a permanent one — see
	// gateway/threads.go's handlePromoteThread. A one-line UPDATE
	// server-side (the row/messages/events already exist in full, per
	// store.go's ghost schema comment), so this just flips the local
	// isGhostThread flag and refreshes the sidebar/header views that were
	// withheld while it was still ghost, rather than needing to re-fetch
	// or reconstruct anything.
	async promote() {
		if (!this.currentThreadId || !this.isGhostThread) return;
		const id = this.currentThreadId;
		const res = await fetch(`/api/threads/${id}/promote`, { method: 'POST' });
		if (!res.ok) {
			this.showToast("Couldn't save this chat — please try again");
			return;
		}
		this.isGhostThread = false;
		this.syncURL(id);
		void this.loadThreads();
		void this.refreshCurrentThreadIfMatches(id);
	}

	// Writes through a selector change (model/focus mode/deep research/
	// research toggle) as the current thread's new sticky config, the
	// moment it's changed from ComposerMenu's "+" sheet rather than
	// waiting for the next send() — see store.SetThreadConfig's doc
	// comment. No-ops for a not-yet-created thread (currentThreadId still
	// null): handleTurn's own write-through covers that case once the
	// first message actually creates it.
	async persistThreadConfig(model: string, focusMode: FocusMode, deepResearch: boolean, noResearch: boolean) {
		if (!this.currentThreadId) return;
		await fetch(`/api/threads/${this.currentThreadId}`, {
			method: 'PATCH',
			headers: { 'Content-Type': 'application/json' },
			// 'off' -> '' matches send()'s own wire-format normalization
			// below (ClientMessage.focus_mode) — "no focus mode" is always
			// empty string server-side (see agent.loadSystemPrompt's map
			// lookup), 'off' is only the frontend's own sentinel for it.
			body: JSON.stringify({
				model,
				focus_mode: focusMode === 'off' ? '' : focusMode,
				deep_research: deepResearch,
				no_research: noResearch
			})
		});
	}

	// Toggling favorite doesn't touch updated_at server-side (see
	// store.SetThreadFavorite's doc comment), so re-fetching the list
	// afterward moves the thread between the Favorites/rest sections in
	// Sidebar.svelte without reshuffling its position within either one.
	async favoriteThread(id: string, favorite: boolean) {
		await fetch(`/api/threads/${id}`, {
			method: 'PATCH',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ favorite })
		});
		await this.loadThreads();
		await this.refreshCurrentThreadIfMatches(id);
	}

	// Cancels the in-flight turn. The backend aborts its LLM/tool calls
	// mid-flight and still sends a normal 'done' with whatever streamed so
	// far — no separate "stopped" event type needed, the existing done
	// handler already finalizes the turn correctly.
	//
	// Gated on busyOnCurrentThread, not just busy: the only UI that calls
	// this is the composer's Stop button, which is only ever shown when
	// busyOnCurrentThread is true — but guarding here too means even a
	// stray/future call site can't send a stop that targets whatever
	// thread happens to be pending elsewhere instead of what's on screen.
	stopGeneration() {
		if (!this.busyOnCurrentThread) return;
		this.socket.send({ type: 'stop' });
	}

	// sttCostUsd is set when content came from a transcribed voice memo
	// (already billed via /api/transcribe) so it gets folded into the
	// thread's running total instead of silently untracked. focusMode/
	// deepResearch/attachments come from the composer's "+" menu
	// (ComposerMenu.svelte) — only plumbed through the plain-text send
	// path for now, not retry/editMessage below (same scope boundary
	// sttCostUsd already draws) or VoiceButton's transcribed-memo send.
	// noResearch mirrors deepResearch's shape but is also set explicitly by
	// AskUserQuestionCard's "enable web search" action (passing false) to
	// override the composer's current Research toggle for that one reply.
	send(
		content: string,
		sttCostUsd?: number,
		focusMode?: FocusMode,
		deepResearch?: boolean,
		attachments?: UploadedAttachment[],
		noResearch?: boolean,
		source?: string,
		titleSeed?: string,
		// Ghost mode (issue #67) — set from the composer's ghost toggle,
		// same "composer-local state passed per-send call" shape as
		// noResearch/deepResearch above, not a global AppState flag. Never
		// passed by retry()/editMessage() below: ghost mode doesn't support
		// retry/edit in v1 (nothing persisted to fork from).
		ghostMode?: boolean,
		// voiceMode: set only by Transponder (see
		// components/Transponder.svelte) for every turn made during a call
		// — see gateway/protocol.go's ClientMessage.VoiceMode doc comment.
		voiceMode?: boolean,
		// modelOverride: bypasses this.selectedModel (the chat model picker's
		// own ambient state) for this one send — only used by
		// startWeaverThread below, which needs Constellation's own
		// configured model rather than whatever the main assistant's picker
		// last had selected. Every other caller leaves this undefined and
		// gets the normal this.selectedModel behavior, unchanged.
		modelOverride?: string,
		// focusModeManual: true only when the operator just picked
		// focusMode for this specific message from ComposerMenu's Focus
		// picker (see ChatView.svelte's focusModeManual state) — every
		// other caller (retry, suggestion chips, Transponder, Weaver,
		// Pulsar Daily's expand-to-chat) leaves this false/undefined,
		// which is correct: none of those are a live manual composer pick,
		// so Oracle's own focus check should stay free to run. See
		// gateway/protocol.go's ClientMessage.FocusModeSource doc comment.
		focusModeManual?: boolean
	) {
		const trimmed = content.trim();
		if (!trimmed || this.busy) return;
		this.dispatch(
			trimmed,
			undefined,
			undefined,
			sttCostUsd,
			focusMode,
			deepResearch,
			attachments,
			noResearch,
			source,
			titleSeed,
			ghostMode,
			voiceMode,
			modelOverride,
			focusModeManual
		);
	}

	// startWeaverThread (issue #94, "Talk to Weaver") starts a brand-new
	// thread with source: 'weaver' — the same client-supplied-source
	// mechanism Pulsar Daily's expand-to-chat already uses for
	// "pulsar-daily" (see dispatch()'s own doc comment on source). Once
	// gateway/turn.go sees that source on thread creation, it runs
	// Weaver's own agent loop instead of the main assistant's for this
	// turn and every later one in the same thread (keyed off the thread's
	// own persisted source, not anything this client has to keep resending
	// — see turn.go's isWeaverThread). model is passed in by the caller
	// (constellationState.config?.model, falling through to undefined —
	// i.e. this.selectedModel — the same "empty means use the default"
	// convention store.ConstellationConfig.Model itself already documents)
	// rather than read from constellationState directly here, to keep
	// AppState from depending on Constellation's own feature-specific
	// state module.
	startWeaverThread(content: string, model?: string) {
		this.send(
			content,
			undefined,
			undefined,
			undefined,
			undefined,
			undefined,
			'weaver',
			undefined,
			undefined,
			undefined,
			model
		);
	}

	// Re-runs an assistant turn using the same preceding user message —
	// most useful after a transient error (network blip, provider hiccup).
	// focusOverride/noOracle: TurnInfoSheet.svelte's "Rerun as X"/"Rerun
	// without Oracle" buttons (docs/plans/oracle-mode.md) — a plain retry
	// button call (ChatTurnView's footer icon) passes neither, same as
	// before either existed. focusOverride is sent as a manual pick (same
	// "operator explicitly chose this for this message" semantics as a
	// live ComposerMenu tap — see send()'s focusModeManual doc comment),
	// not a "default", so Oracle's own focus check won't immediately
	// re-override the very mode being tested.
	// noVisuals: TurnInfoSheet's "Rerun as plain text" — Prism off for this one
	// turn (see gateway/protocol.go's ClientMessage.NoVisuals).
	retry(assistantTurnIndex: number, focusOverride?: FocusMode, noOracle?: boolean, noVisuals?: boolean) {
		const userTurn = this.turns[assistantTurnIndex - 1];
		if (!userTurn || userTurn.role !== 'user' || userTurn.id === undefined || this.busy) return;
		this.dispatch(
			userTurn.content,
			userTurn.id,
			assistantTurnIndex - 1,
			undefined,
			focusOverride,
			undefined,
			undefined,
			undefined,
			undefined,
			undefined,
			undefined,
			undefined,
			undefined,
			!!focusOverride,
			noOracle,
			noVisuals
		);
		this.carryForwardAttachmentChips(userTurn.attachments);
	}

	// Replaces a user message with revised text and re-runs from there.
	editMessage(userTurnIndex: number, newContent: string) {
		const trimmed = newContent.trim();
		const userTurn = this.turns[userTurnIndex];
		if (!trimmed || !userTurn || userTurn.role !== 'user' || userTurn.id === undefined || this.busy) return;
		this.dispatch(trimmed, userTurn.id, userTurnIndex);
		this.carryForwardAttachmentChips(userTurn.attachments);
	}

	// retry()/editMessage() can't pass the original attachments through
	// dispatch()'s own attachments param — that expects fresh
	// UploadedAttachments (with an upload id to send the server), which
	// don't exist here: the server now carries the original message's
	// already-resolved attachments forward on its own (see
	// gateway/turn.go's EditFromID handling), so nothing needs re-sending.
	// This only has to fix the optimistic local display, which dispatch()
	// otherwise leaves attachment-less — a real gap found live: retrying a
	// message that had files attached made the chips vanish from the UI
	// even though the backend (once fixed) kept them. Mutates
	// pendingUserTurn directly, the same post-push pattern dispatch()
	// itself documents, since dispatch() already pushed the plain object
	// literal through Svelte 5's reactive array proxy by the time this runs.
	private carryForwardAttachmentChips(attachments: ChatTurn['attachments']) {
		if (attachments?.length && this.pendingUserTurn) {
			this.pendingUserTurn.attachments = attachments;
		}
	}

	// Shared by send/retry/editMessage: truncate everything from
	// truncateFromIndex onward (if this is a retry/edit), push a fresh
	// user + streaming-assistant pair, and send over the socket.
	// editFromId tells the server which persisted message (and everything
	// after it) to delete before treating content as the replacement.
	private dispatch(
		content: string,
		editFromId?: number,
		truncateFromIndex?: number,
		sttCostUsd?: number,
		focusMode?: FocusMode,
		deepResearch?: boolean,
		attachments?: UploadedAttachment[],
		noResearch?: boolean,
		// source: only meaningful for a brand-new thread (see
		// gateway/protocol.go's ClientMessage.Source) — undefined means
		// the server's own "web" default, same as every caller before
		// this param existed. Only Pulsar Daily's expand-to-chat passes
		// "pulsar-daily" here (see routes/daily/+page.svelte's expand()).
		source?: string,
		// titleSeed: see gateway/protocol.go's ClientMessage.TitleSeed —
		// only Pulsar Daily's expand-to-chat sets this.
		titleSeed?: string,
		// ghostMode: see send()'s doc comment.
		ghostMode?: boolean,
		// voiceMode: see send()'s doc comment.
		voiceMode?: boolean,
		// modelOverride: see send()'s doc comment.
		modelOverride?: string,
		// focusModeManual: see send()'s doc comment.
		focusModeManual?: boolean,
		// noOracle: only ever set by retry()'s "Rerun without Oracle" —
		// see gateway/protocol.go's ClientMessage.NoOracle doc comment.
		// Not exposed through send() itself since no composer control sets
		// it; TurnInfoSheet.svelte's rerun buttons go through retry(),
		// which calls this directly.
		noOracle?: boolean,
		// noVisuals: only ever set by retry()'s "Rerun as plain text" — see
		// gateway/protocol.go's ClientMessage.NoVisuals doc comment.
		noVisuals?: boolean
	) {
		if (truncateFromIndex !== undefined) {
			this.turns = this.turns.slice(0, truncateFromIndex);
		}
		this.suggestions = [];

		// Ghost mode (issue #67) is sticky for the whole session, not just
		// whatever this one call happened to pass: this.isGhostThread
		// being already true means an earlier turn on this same thread
		// went ghost, and every later turn MUST stay ghost too regardless
		// of which UI control fired it — a real bug caught live, not just
		// a theoretical one: the follow-up suggestion chips' own click
		// handler (appState.send(suggestion), ChatView.svelte) never
		// passes ghostMode at all, so without this, clicking a suggestion
		// mid-ghost-conversation would silently drop back to a normal
		// turn. newThread()/openThread() are the only things that clear
		// isGhostThread, so this can't accidentally stay stuck across an
		// unrelated later session.
		const isGhost = !!ghostMode || this.isGhostThread;

		this.turns.push({
			role: 'user',
			content,
			attachments: attachments?.map((a) => ({ filename: a.filename, content_type: a.content_type }))
		});
		this.turns.push({ role: 'assistant', content: '', timeline: [], streaming: true });
		this.busy = true;

		// Read the pushed turns back out of the reactive array instead of
		// holding the plain object literals passed to push() — Svelte 5's
		// $state wraps array contents in a reactive proxy, and mutating the
		// pre-wrap object reference (what push() was originally given)
		// bypasses that proxy entirely: the mutation "succeeds" in that the
		// data is technically correct, but no re-render is ever scheduled
		// for it, since Svelte only tracks writes made *through* the proxy.
		// The whole point of pendingTurn is to be mutated live from
		// handleEvent below, so it must be the proxied element, not the
		// literal that was pushed.
		this.pendingUserTurn = this.turns[this.turns.length - 2];
		this.pendingTurn = this.turns[this.turns.length - 1];
		// A ghost thread now gets a real, server-assigned id exactly like
		// any other brand-new thread (see store.go's ghost schema comment)
		// — no client-minted id needed — so this needs no isGhost branch:
		// null on a brand-new thread (ghost or not), learned from the first
		// streamed event via the "brand-new thread just learned its id"
		// block at the top of handleEvent below.
		this.pendingThreadId = this.currentThreadId;
		this.pendingIsNewThread = this.currentThreadId === null;
		this.pendingGhost = isGhost;
		this.pendingAbandoned = false;

		debugBeacon('dispatch sending', {
			currentThreadId: this.currentThreadId,
			editFromId,
			truncateFromIndex,
			turnsLengthBeforePush: truncateFromIndex !== undefined ? truncateFromIndex : this.turns.length - 2
		});

		this.socket.send({
			type: 'message',
			thread_id: this.currentThreadId ?? undefined,
			content,
			model: modelOverride ?? this.selectedModel,
			edit_from_id: editFromId,
			stt_cost_usd: sttCostUsd,
			user_location: getUserLocation(),
			focus_mode: focusMode && focusMode !== 'off' ? focusMode : undefined,
			// Omitted (manual) only for a live composer pick — see
			// send()'s focusModeManual doc comment and
			// gateway/protocol.go's ClientMessage.FocusModeSource.
			focus_mode_source: focusModeManual ? undefined : 'default',
			deep_research: deepResearch || undefined,
			no_research: noResearch || undefined,
			no_oracle: noOracle || undefined,
			no_visuals: noVisuals || undefined,
			attachments: attachments?.map((a) => ({
				id: a.id,
				filename: a.filename,
				content_type: a.content_type
			})),
			source,
			// Only a brand-new thread is bound at creation; a later turn's
			// stray value is ignored server-side too (see gateway/protocol.go).
			field_id: this.currentThreadId === null ? (this.pendingFieldId ?? undefined) : undefined,
			title_seed: titleSeed,
			anonymous: isGhost || undefined,
			voice_mode: voiceMode || undefined
		});
	}

	private handleEvent(e: ServerEvent) {
		const eventThreadId = 'thread_id' in e ? e.thread_id : undefined;

		// A brand-new thread's ID isn't known until the server assigns one.
		// Sync the URL the instant it is — not currentThreadId itself,
		// which "done" below still gates on stillWatching — so a refresh
		// partway through the very first answer still reopens this thread
		// instead of losing it entirely (there'd be no ID to recover by).
		// Withheld for a ghost turn: the address bar must stay off this
		// thread's real id for as long as it's still unpromoted (a ghost
		// thread does have one now — see store.go's ghost schema comment —
		// it just isn't meant to be individually addressable yet).
		if (this.pendingIsNewThread && this.pendingThreadId === null && eventThreadId) {
			this.pendingThreadId = eventThreadId;
			if (!this.pendingGhost) this.syncURL(eventThreadId);
		}

		// 'suggestions' arrives well after 'done', which already cleared
		// pendingThreadId/pendingTurn — the "still tracking this turn" gate
		// just below (and the pendingTurn check further down) both exist to
		// filter events belonging to an in-flight turn, which this isn't
		// anymore by the time it shows up. Compare against currentThreadId
		// directly instead, same as swapVariant/openThread do, and handle
		// it here before that gate would otherwise drop it.
		if (e.type === 'suggestions') {
			if (eventThreadId === this.currentThreadId) {
				this.totalCost += e.cost_usd ?? 0;
				this.suggestions = e.suggestions ?? [];
			}
			return;
		}

		// 'verification' arrives even later than 'suggestions' — same
		// "already past every in-flight-turn gate" situation, but unlike
		// suggestions (which just replaces a flat list tied to whatever's
		// most recent) this has to find the *specific* message it belongs
		// to: the user may have sent another message, or even navigated to
		// a different thread, by the time it lands.
		if (e.type === 'verification') {
			if (eventThreadId === this.currentThreadId) {
				const turn = this.turns.find((t) => t.id === e.assistant_message_id);
				if (turn) {
					turn.verification = e.verification;
					turn.citations = applyVerification(turn.citations, e.verification);
				}
			}
			return;
		}

		// Not for the turn we're tracking — most likely a late event for a
		// turn the user has since navigated away from. The backend is
		// still persisting it independently regardless; reopening that
		// thread later will show the finished result. Just don't let it
		// touch whatever's currently on screen.
		if (eventThreadId && eventThreadId !== this.pendingThreadId) return;

		if (e.type === 'user_message') {
			if (this.pendingUserTurn) this.pendingUserTurn.id = e.user_message_id;
			// The thread row (and this user message) are already persisted
			// server-side by the time this event fires — well before the LLM
			// call even starts, let alone finishes. Refresh the sidebar now
			// instead of waiting for "done", so a brand-new thread appears
			// (and an existing one jumps to the top) within one round trip
			// of hitting send, not after the whole answer streams in. Skipped
			// for a ghost turn — the thread row is real now, but still
			// excluded from ListThreads while unpromoted (see store.go's
			// ghost schema comment), so refreshing the sidebar for it would
			// be pure waste (it could never appear there yet).
			if (!this.pendingGhost) void this.loadThreads();
			return;
		}

		// nearby_search or weather wants a live GPS fix mid-turn (see
		// geolocation.ts's requestFreshLocation and gateway/protocol.go's
		// "location_request" doc comment). This is the only place the app
		// ever touches navigator.geolocation — no page-load prime, no
		// timer — so the browser's location is asked for exactly when a
		// tool call actually needs it, not for as long as the tab happens
		// to be open. Always reply, even empty: the server is already
		// waiting on this and treats "no answer" as a normal fallback, not
		// something worth stalling the turn over.
		if (e.type === 'location_request') {
			void requestFreshLocation().then((loc) => {
				this.socket.send({ type: 'location_response', user_location: loc || undefined });
			});
			return;
		}

		const turn = this.pendingTurn;
		if (!turn) return;

		switch (e.type) {
			case 'compacted':
				// Arrives at the START of the turn after the one that
				// triggered it, not during the turn that did — compaction is
				// detached from that turn's "done" (see gateway/turn.go), so
				// this is the first moment there's a live client to tell.
				// Same delayed-carrier shape as 'suggestions'/'verification',
				// and cost_usd is the compaction call's own spend, added to
				// the running total exactly like 'done''s — it is not part of
				// any "done" event, since it hadn't happened when that
				// shipped. Unlike those two this needs no special placement
				// above the in-flight gate: it belongs to the turn that is
				// genuinely in flight right now, so the gate it sits behind
				// is exactly the right one.
				closeOpenReasoning(turn);
				this.totalCost += e.cost_usd ?? 0;
				turn.timeline = [...(turn.timeline ?? []), { kind: 'compacted', summary: e.content }];
				break;

			case 'done': {
				closeOpenReasoning(turn);
				turn.streaming = false;
				turn.citations = e.citations;
				turn.cards = e.cards;
				turn.chart = e.chart;
				turn.pendingQuestion = e.pending_question;
				turn.costUsd = e.cost_usd ?? 0;
				turn.durationMs = e.duration_ms;
				// Oracle mode — see ServerEvent's 'done' doc comment. Both
				// undefined whenever Oracle didn't run this turn, same
				// silent-normal-outcome convention as pendingQuestion above.
				turn.oracleResult = e.oracle_result;
				turn.oracleFocusModeSource = e.oracle_focus_mode_source;
				turn.costAnswer = e.cost_answer_usd;
				turn.costVerification = e.cost_verification_usd;
				turn.costOracle = e.cost_oracle_usd;
				turn.promptTokens = e.prompt_tokens;
				turn.cacheReadTokens = e.cache_read_tokens;
				turn.completionTokens = e.completion_tokens;
				turn.lastPromptTokens = e.last_prompt_tokens;
				turn.llmCalls = e.llm_calls;
				turn.toolCallCount = e.tool_call_count;
				turn.ttftMs = e.ttft_ms;
				turn.tokensPerSecond = e.tokens_per_second;
				turn.appliedFocusMode = e.applied_focus_mode;
				turn.appliedModel = e.applied_model;
				// See ServerEvent's assistant_message_id doc comment — without
				// this, read-aloud on a turn from the current session (not yet
				// reloaded from history) has no message id to attach a
				// persisted audio file to.
				turn.id = e.assistant_message_id;
				this.busy = false;
				// Captured before the pendingGhost reset below, since both
				// branches below (and the loadThreads gate further down)
				// need to know what this turn actually was.
				const wasGhost = this.pendingGhost;
				// Only adopt the thread id / bump the visible total if the
				// user is still looking at this thread (or it just became
				// one) — not if they've since navigated elsewhere.
				// pendingAbandoned is what actually distinguishes those two
				// cases now: currentThreadId === null is true for BOTH
				// "still on the brand-new thread this turn is creating" and
				// "explicitly backed out via newThread() while this turn
				// kept running" — see pendingAbandoned's doc comment.
				const stillWatching =
					!this.pendingAbandoned &&
					(this.currentThreadId === null || this.currentThreadId === this.pendingThreadId);
				debugBeacon('done event received', {
					stillWatching,
					wasGhost,
					pendingAbandoned: this.pendingAbandoned,
					currentThreadId: this.currentThreadId,
					pendingThreadId: this.pendingThreadId,
					eventThreadId: e.thread_id
				});
				if (stillWatching) {
					// There's always a real id now, ghost or not — see
					// store.go's ghost schema comment — so this adopts
					// unconditionally rather than branching on wasGhost the
					// way the old client-minted-id design had to.
					this.currentThreadId = e.thread_id;
					this.isGhostThread = wasGhost;
					// ?? 0 guards against a missing cost_usd (e.g. an older
					// cached frontend bundle talking to a newer backend, or
					// vice versa) turning totalCost into a sticky NaN that
					// poisons every subsequent addition for the rest of the
					// session — this exact bug shipped once already.
					this.totalCost += e.cost_usd ?? 0;
					if (e.context_tokens !== undefined) this.contextTokens = e.context_tokens;
					this.promptTokens += e.prompt_tokens ?? 0;
					this.cacheReadTokens += e.cache_read_tokens ?? 0;
					// Cleared here, not filled in — follow-up suggestions are
					// a separate LLM call the backend now runs after "done"
					// ships (see protocol.go's doc comment on the "suggestions"
					// event type) precisely so the turn footer doesn't stall
					// waiting on them. They arrive moments later via the
					// 'suggestions' case below and render underneath the
					// footer that's already visible.
					this.suggestions = [];
					// Both of these hit /api/threads/{id}, which 404s for a
					// still-ghost thread (see store.go's ghost schema
					// comment) — skipped while ghost rather than firing a
					// request that could only ever come back empty.
					if (!wasGhost) {
						// An edit/retry that just finished may have forked a
						// new variant into existence — ServerEvent carries no
						// variants field (openThread/swapVariant are the only
						// other places appState.variants gets set), so
						// without this the switcher stayed invisible until
						// the thread was closed and reopened, even though the
						// fork existed correctly server-side the whole time.
						// Harmless no-op for a plain send: the variants map
						// just comes back the same as before.
						void this.refreshVariants(e.thread_id);
						// A brand-new thread's first turn (or a first-message
						// edit) just got its one-time LLM-generated title
						// persisted server-side (see gateway/turn.go's
						// isNewThread/isFirstMessageEdit title-gating block)
						// — but currentThread itself was never populated for
						// this flow (dispatch()/send() only ever set
						// currentThreadId, not currentThread; only
						// openThread() does that, normally on navigating to
						// an *existing* thread). Without this,
						// ChatView.svelte's header (which reads
						// appState.currentThread.title, not the sidebar's
						// already-refreshed `threads` list, to also cover a
						// pulsar thread the list excludes) silently kept
						// showing no title at all until the thread was closed
						// and reopened, even though the real title existed
						// server-side the whole time. Harmless no-op on every
						// other turn: the row comes back the same as before.
						void this.refreshCurrentThreadIfMatches(e.thread_id);
					}
				}
				this.pendingTurn = null;
				this.pendingUserTurn = null;
				this.pendingThreadId = null;
				// Real bug caught live: this used to NOT reset
				// pendingIsNewThread here — its only job is describing
				// whatever turn was just in flight, but a stray, LATER event
				// for the very same turn (the 'suggestions' event, sent
				// "shortly after done" per its own doc comment above) would
				// still see pendingIsNewThread=true and pendingThreadId=null
				// (just cleared right here) and wrongly re-trigger this
				// function's top-of-handleEvent "brand-new thread just
				// learned its id" branch a second time — for a normal thread
				// that's harmless (syncURL no-ops against a path it already
				// set), but for a ghost turn it meant the suggestions event
				// alone synced the address bar to the ghost session's real
				// id, moments after the 'done' handling above had correctly
				// avoided doing exactly that.
				this.pendingIsNewThread = false;
				this.pendingGhost = false;
				// Skipped for a ghost turn — there's no thread row that
				// could have been created or bumped for the sidebar to show.
				if (!wasGhost) void this.loadThreads();
				// Retries a version-change reload checkVersion() deferred
				// while this turn was in flight (see its doc comment) —
				// without this, a build that landed mid-turn wouldn't be
				// noticed again until the next 30s poll happens to land.
				void this.checkVersion();
				break;
			}

			case 'error':
				closeOpenReasoning(turn);
				turn.streaming = false;
				if (e.error_kind === 'network') {
					// No raw Go error text on this turn — ChatTurnView
					// renders its own "connection lost" banner off errorKind
					// instead of turn.content.
					turn.errorKind = 'network';
				} else if (!turn.content) {
					turn.content = `Error: ${e.message}`;
				}
				this.busy = false;
				this.pendingTurn = null;
				this.pendingUserTurn = null;
				this.pendingThreadId = null;
				this.pendingIsNewThread = false;
				this.pendingGhost = false;
				void this.checkVersion();
				break;
			default:
				applyStreamingEvent(turn, e);
				break;
		}
	}
}

export const appState = new AppState();
