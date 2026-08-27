import type {
	ChatTurn,
	ModelOption,
	Thread,
	ServerEvent,
	Citation,
	Card,
	StoredEvent,
	TimelineItem,
	FocusMode,
	UploadedAttachment,
	VariantGroup,
	MessageSearchResult
} from './types';
import { AgentSocket } from './ws';
import { AudioPlayer } from './audio.svelte';
import { SettingsState } from './settings.svelte';
import { getUserLocation, requestFreshLocation } from './geolocation';

function safeParseJSON<T>(json: string): T[] {
	try {
		return JSON.parse(json) ?? [];
	} catch {
		return [];
	}
}

function safeParseObject(json: string): Record<string, any> {
	try {
		return JSON.parse(json) ?? {};
	} catch {
		return {};
	}
}

// TEMPORARY instrumentation for chasing the "thread bump-back" bug (see
// memory: project_thread_bump_back_root_cause) — fires a fire-and-forget
// beacon to the server's event log at the handful of places
// currentThreadId changes or a version-mismatch reload fires, so the next
// occurrence can be read back from the events table afterward instead of
// needing the user to have DevTools open at the exact moment it happens.
// keepalive (not navigator.sendBeacon) is what survives the page unloading
// (the exact moment a reload/href navigation fires) here — sendBeacon
// would do the same in a real browser, but its rejection can't be caught
// the way a plain fetch promise's can, which surfaced as unhandled
// rejections under happy-dom's polyfill. Remove this and its call sites
// once the mechanism is confirmed and fixed.
export function debugBeacon(message: string, data: Record<string, unknown> = {}) {
	if (typeof fetch === 'undefined') return;
	fetch('/api/debug-log', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		keepalive: true,
		body: JSON.stringify({ message, data })
	}).catch(() => {
		// Best-effort — never let diagnostics themselves break the app.
	});
}

// Rebuilds one turn's timeline from its persisted events (thinking steps,
// tool call start/finish pairs, compaction) — the same shape handleEvent
// builds live while a turn streams, so a reopened thread renders
// identically to one that's still on screen. events must be this turn's
// slice only, oldest-first (see ListEvents' ORDER BY id ASC).
function buildTimelineFromEvents(events: StoredEvent[]): TimelineItem[] {
	const timeline: TimelineItem[] = [];
	for (const evt of events) {
		const data = safeParseObject(evt.data);
		if (evt.source === 'turn' && evt.message === 'thinking') {
			timeline.push({ kind: 'thinking', content: data.content ?? '' });
		} else if (evt.source === 'turn' && evt.message === 'commentary') {
			timeline.push({ kind: 'commentary', content: data.content ?? '' });
		} else if (evt.source === 'turn' && evt.message === 'reasoning') {
			// Persisted as one row per burst (see gateway/turn.go's
			// flushReasoning), already complete — done: true, unlike the
			// live-streaming case where a burst starts as done: false and
			// gets closed out by closeOpenReasoning once something else
			// interrupts it.
			timeline.push({ kind: 'reasoning', content: data.content ?? '', done: true });
		} else if (evt.source === 'compaction' && evt.message === 'thread auto-compacted') {
			timeline.push({ kind: 'compacted', summary: data.summary ?? '' });
		} else if (evt.source.startsWith('tool.')) {
			const tool = evt.source.slice('tool.'.length);
			if (evt.message === 'tool call started') {
				timeline.push({ kind: 'tool', tool, args: data.args, done: false });
			} else if (evt.message === 'tool call finished') {
				for (let i = timeline.length - 1; i >= 0; i--) {
					const item = timeline[i];
					if (item.kind === 'tool' && item.tool === tool && !item.done) {
						timeline[i] = { ...item, result: data.result, citations: data.citations, done: true };
						break;
					}
				}
			}
		}
	}
	return timeline;
}

// Exported (not just the singleton below) so tests can construct fresh,
// isolated instances instead of sharing the one live during a real
// session.
export class AppState {
	turns = $state<ChatTurn[]>([]);
	threads = $state<Thread[]>([]);

	// Sidebar's "search past chats" box — see searchThreads' doc comment.
	// Separate from `threads` (the plain recency list) rather than
	// filtering it client-side, since this searches every message's full
	// content via the server's FTS5 index, not just what's already loaded
	// here.
	threadSearchQuery = $state('');
	threadSearchResults = $state<MessageSearchResult[]>([]);
	threadSearchLoading = $state(false);
	models = $state<ModelOption[]>([]);
	selectedModel = $state<string>('');
	currentThreadId = $state<string | null>(null);
	connected = $state(false);
	busy = $state(false);
	totalCost = $state(0);
	version = $state<string>('');
	// 'bare-metal' | 'docker' | '' (not yet loaded) — see gateway/version.go's
	// deploymentMode. Purely a display signal for the settings panel's
	// version-row icon, set alongside version in checkVersion() below.
	deployment = $state<string>('');
	versionCheckInterval: number | null = null;

	// contextTokens is the current thread's last-known prompt+completion
	// size, from the LLM's own usage numbers — settings.contextWindowTokens
	// (the auto-compaction threshold) is the denominator for the % shown
	// next to it in +page.svelte.
	contextTokens = $state(0);

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

	// Brief, app-level confirmation banners — first use is the copy
	// buttons in ChatTurnView.svelte, where the per-button checkmark swap
	// alone turned out to not be a clear enough "yes, that worked" signal
	// on its own. A plain array (not a single "current toast") so two
	// quick actions don't cut each other off mid-fade.
	toasts = $state<{ id: number; message: string }[]>([]);
	private nextToastId = 0;

	showToast(message: string, durationMs = 2000) {
		const id = this.nextToastId++;
		this.toasts = [...this.toasts, { id, message }];
		setTimeout(() => {
			this.toasts = this.toasts.filter((t) => t.id !== id);
		}, durationMs);
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

	// searchThreads' debounce timer/cancellation state — see its own doc
	// comment for why both the timer and the seq/AbortController pair are
	// needed together.
	private threadSearchSeq = 0;
	private threadSearchController: AbortController | null = null;
	private threadSearchDebounce: ReturnType<typeof setTimeout> | null = null;

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
			return;
		}

		await this.openThread(threadId);
		this.busy = false;
		this.pendingTurn = null;
		this.pendingUserTurn = null;
		this.pendingThreadId = null;
		void this.loadThreads();
	}

	connect() {
		this.socket.connect();
		void this.startVersionCheck();
	}

	async startVersionCheck() {
		// Check version immediately on connect
		await this.checkVersion();

		// Then poll every 30 seconds
		if (typeof window !== 'undefined') {
			this.versionCheckInterval = window.setInterval(() => {
				void this.checkVersion();
			}, 30000);
		}
	}

	async checkVersion() {
		try {
			const res = await fetch('/api/version');
			const data = await res.json();
			const newVersion = data.version ?? '';
			// Static for the process's whole lifetime (only a real restart
			// changes it) — fine to just assign unconditionally on every
			// poll, unlike version's mismatch-triggers-a-reload dance below.
			this.deployment = data.deployment ?? '';

			if (this.version && newVersion && this.version !== newVersion) {
				debugBeacon('checkVersion mismatch detected', {
					oldVersion: this.version,
					newVersion,
					busy: this.busy,
					currentThreadId: this.currentThreadId
				});
				// A new build landed — but reloading immediately would yank
				// an in-flight turn out from under the user: it wipes
				// busy/pendingTurn/pendingThreadId client-side while the
				// turn keeps running server-side regardless. Deferring
				// until nothing's in flight — and deliberately NOT updating
				// this.version below so this same branch re-fires — is what
				// makes the reload land at a safe moment. handleEvent's
				// 'done'/'error' cases call this again the instant busy
				// clears, so the retry happens within moments of the turn
				// finishing rather than waiting out the rest of this 30s
				// poll interval.
				//
				// Navigating to an explicit href (not a bare reload())
				// matters: a bare reload() trusts window.location.pathname
				// to already reflect whatever thread is actually current,
				// but syncURL's replaceState calls only fire from specific
				// call sites (openThread, newThread, a just-learned new
				// thread id) — 'done' itself never re-syncs the URL, so any
				// path where the address bar and currentThreadId can
				// legitimately drift apart for a moment (confirmed
				// happening in practice, not just theoretical) turns into
				// reload() silently landing on whatever the browser's
				// address bar happened to still say, which can be a
				// completely unrelated thread from earlier in the session
				// rather than "the homescreen" this comment used to assume.
				// Building the URL explicitly from currentThreadId — the
				// same source of truth syncURL itself uses — removes that
				// gap by construction instead of relying on timing.
				if (!this.busy && typeof window !== 'undefined') {
					const path = this.currentThreadId ? `/t/${this.currentThreadId}` : '/';
					debugBeacon('checkVersion reloading', {
						oldVersion: this.version,
						newVersion,
						currentThreadId: this.currentThreadId,
						currentPathname: window.location.pathname,
						targetPath: path
					});
					// Checked before navigating, not after: whether an href
					// assignment updates window.location synchronously or
					// only once the new document actually loads isn't
					// consistent across environments (confirmed different
					// between real browsers and jsdom), so branching on the
					// current path up front is the only deterministic way
					// to pick reload() vs. href — see this block's doc
					// comment above for why the target must be explicit.
					if (window.location.pathname === path) {
						window.location.reload();
					} else {
						window.location.href = path;
					}
				}
				return;
			}
			this.version = newVersion;
		} catch (err) {
			// Silently ignore - don't spam errors for version checks
		}
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

	// Debounced, cancellable full-text search over past chat content
	// (GET /api/threads/search) — called on every keystroke in the
	// sidebar's search box, so both a client-side debounce (this doesn't
	// fire a request per character) and the seq/AbortController guard
	// (a slow response for an earlier keystroke can't clobber a faster
	// one for a later keystroke) matter here, same reasoning as
	// SearchState.search() in search.svelte.ts.
	searchThreads(query: string) {
		this.threadSearchQuery = query;
		if (this.threadSearchDebounce !== null) clearTimeout(this.threadSearchDebounce);

		const trimmed = query.trim();
		if (!trimmed) {
			this.threadSearchController?.abort();
			this.threadSearchResults = [];
			this.threadSearchLoading = false;
			return;
		}

		this.threadSearchLoading = true;
		this.threadSearchDebounce = setTimeout(() => void this.runThreadSearch(trimmed), 250);
	}

	private async runThreadSearch(query: string) {
		this.threadSearchController?.abort();
		const controller = new AbortController();
		this.threadSearchController = controller;
		const seq = ++this.threadSearchSeq;

		try {
			const res = await fetch(`/api/threads/search?q=${encodeURIComponent(query)}`, {
				signal: controller.signal
			});
			if (seq !== this.threadSearchSeq) return; // superseded by a newer keystroke
			this.threadSearchResults = res.ok ? ((await res.json()) ?? []) : [];
		} catch {
			if (seq !== this.threadSearchSeq) return; // includes our own abort() above
			this.threadSearchResults = [];
		} finally {
			if (seq === this.threadSearchSeq) this.threadSearchLoading = false;
		}
	}

	// Clears the search box back to the plain recency-ordered thread list —
	// called by the box's own clear button and when a result is clicked
	// (opening a thread shouldn't leave a stale search sitting above it).
	clearThreadSearch() {
		if (this.threadSearchDebounce !== null) clearTimeout(this.threadSearchDebounce);
		this.threadSearchController?.abort();
		this.threadSearchQuery = '';
		this.threadSearchResults = [];
		this.threadSearchLoading = false;
		++this.threadSearchSeq;
	}

	// Shared by openThread and swapVariant — both end up with the exact
	// same GetThread response shape (see gateway/threads.go's
	// handleGetThread/handleSwapVariant) and need to turn it into the same
	// ChatTurn[]/suggestions/variants state, just triggered differently
	// (navigating to a thread vs. browsing to a different reply within
	// the one already open).
	private async fetchEventsByTurn(id: string): Promise<Map<string, StoredEvent[]>> {
		const eventsByTurn = new Map<string, StoredEvent[]>();
		const eventsRes = await fetch(`/api/threads/${id}/events`);
		if (eventsRes.ok) {
			const events: StoredEvent[] = (await eventsRes.json()) ?? [];
			for (const evt of events) {
				if (!evt.turn_id) continue;
				const group = eventsByTurn.get(evt.turn_id);
				if (group) group.push(evt);
				else eventsByTurn.set(evt.turn_id, [evt]);
			}
		}
		return eventsByTurn;
	}

	private buildTurnsFromMessages(messages: any[], eventsByTurn: Map<string, StoredEvent[]>): ChatTurn[] {
		return messages.map((m: any) => ({
			role: m.role,
			content: m.content,
			citations: safeParseJSON<Citation>(m.citations),
			cards: safeParseJSON<Card>(m.cards),
			costUsd: m.cost_usd,
			durationMs: m.duration_ms || undefined,
			id: m.role === 'user' ? m.id : undefined,
			attachmentFilename: m.attachment_filename || undefined,
			attachmentContentType: m.attachment_content_type || undefined,
			timeline:
				m.role === 'assistant' && m.turn_id && eventsByTurn.has(m.turn_id)
					? buildTimelineFromEvents(eventsByTurn.get(m.turn_id)!)
					: undefined
		}));
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
			[res, eventsByTurn] = await Promise.all([fetch(`/api/threads/${id}`), this.fetchEventsByTurn(id)]);
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
		this.syncURL(id);
		this.totalCost = data.cost_usd ?? 0;
		this.contextTokens = data.context_tokens ?? 0;
		this.variants = data.variants ?? {};
		const messages = data.messages ?? [];

		// Group persisted events by turn_id so each assistant message's
		// timeline (thinking steps, tool calls) can be reattached below —
		// otherwise reopening a thread shows only the bare final answer,
		// with everything that led up to it gone. Older messages predating
		// this feature have turn_id "" and simply get no timeline back.
		let turns = this.buildTurnsFromMessages(messages, eventsByTurn);

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
		}
		this.turns = turns;

		// Suggestions are a "what's next" prompt for the last answer, so
		// only the most recent assistant message's set is relevant here.
		const lastAssistant = [...messages].reverse().find((m: any) => m.role === 'assistant');
		this.suggestions = lastAssistant ? safeParseJSON<string>(lastAssistant.suggestions) : [];
		this.closeSidebarIfMobile();
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
		const eventsByTurn = await this.fetchEventsByTurn(id);
		this.totalCost = data.cost_usd ?? 0;
		this.contextTokens = data.context_tokens ?? 0;
		this.variants = data.variants ?? {};
		const messages = data.messages ?? [];
		this.turns = this.buildTurnsFromMessages(messages, eventsByTurn);
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

	newThread() {
		// Same reasoning as openThread's abandonment check: navigating to
		// "no thread selected" can never match whatever the in-flight
		// turn's thread actually is (even a still-forming brand-new thread
		// whose id isn't known yet, i.e. pendingThreadId is itself still
		// null — restarting the new-thread flow explicitly abandons that
		// one too, not just an existing thread's turn).
		if (this.busy) this.pendingAbandoned = true;

		debugBeacon('currentThreadId set (newThread)', { from: this.currentThreadId, busy: this.busy });
		this.currentThreadId = null;
		this.turns = [];
		this.totalCost = 0;
		this.contextTokens = 0;
		this.suggestions = [];
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
		return true;
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
	// deepResearch/attachment come from the composer's "+" menu
	// (ComposerMenu.svelte) — only plumbed through the plain-text send
	// path for now, not retry/editMessage below (same scope boundary
	// sttCostUsd already draws) or VoiceButton's transcribed-memo send.
	send(content: string, sttCostUsd?: number, focusMode?: FocusMode, deepResearch?: boolean, attachment?: UploadedAttachment) {
		const trimmed = content.trim();
		if (!trimmed || this.busy) return;
		this.dispatch(trimmed, undefined, undefined, sttCostUsd, focusMode, deepResearch, attachment);
	}

	// Re-runs an assistant turn using the same preceding user message —
	// most useful after a transient error (network blip, provider hiccup).
	retry(assistantTurnIndex: number) {
		const userTurn = this.turns[assistantTurnIndex - 1];
		if (!userTurn || userTurn.role !== 'user' || userTurn.id === undefined || this.busy) return;
		this.dispatch(userTurn.content, userTurn.id, assistantTurnIndex - 1);
	}

	// Replaces a user message with revised text and re-runs from there.
	editMessage(userTurnIndex: number, newContent: string) {
		const trimmed = newContent.trim();
		const userTurn = this.turns[userTurnIndex];
		if (!trimmed || !userTurn || userTurn.role !== 'user' || userTurn.id === undefined || this.busy) return;
		this.dispatch(trimmed, userTurn.id, userTurnIndex);
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
		attachment?: UploadedAttachment
	) {
		if (truncateFromIndex !== undefined) {
			this.turns = this.turns.slice(0, truncateFromIndex);
		}
		this.suggestions = [];
		this.turns.push({
			role: 'user',
			content,
			attachmentFilename: attachment?.filename,
			attachmentContentType: attachment?.content_type
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
		this.pendingThreadId = this.currentThreadId;
		this.pendingIsNewThread = this.currentThreadId === null;
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
			model: this.selectedModel,
			edit_from_id: editFromId,
			stt_cost_usd: sttCostUsd,
			user_location: getUserLocation(),
			focus_mode: focusMode && focusMode !== 'off' ? focusMode : undefined,
			deep_research: deepResearch || undefined,
			attachment_id: attachment?.id,
			attachment_filename: attachment?.filename,
			attachment_content_type: attachment?.content_type
		});
	}

	// Reasoning always finishes before the visible answer (or a tool call)
	// starts, per OpenRouter's ordering guarantee — so whenever something
	// else is about to land on the timeline, mark any still-open reasoning
	// item done first, so its UI stops showing a live/streaming state.
	private closeOpenReasoning(turn: ChatTurn) {
		const items = turn.timeline;
		if (!items || items.length === 0) return;
		const last = items[items.length - 1];
		if (last.kind === 'reasoning' && !last.done) {
			last.done = true;
			turn.timeline = [...items];
		}
	}

	private handleEvent(e: ServerEvent) {
		const eventThreadId = 'thread_id' in e ? e.thread_id : undefined;

		// A brand-new thread's ID isn't known until the server assigns one.
		// Sync the URL the instant it is — not currentThreadId itself,
		// which "done" below still gates on stillWatching — so a refresh
		// partway through the very first answer still reopens this thread
		// instead of losing it entirely (there'd be no ID to recover by).
		if (this.pendingIsNewThread && this.pendingThreadId === null && eventThreadId) {
			this.pendingThreadId = eventThreadId;
			this.syncURL(eventThreadId);
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
			// of hitting send, not after the whole answer streams in.
			void this.loadThreads();
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
			case 'thinking':
				this.closeOpenReasoning(turn);
				turn.timeline = [...(turn.timeline ?? []), { kind: 'thinking', content: e.content }];
				break;

			case 'reasoning': {
				const items = turn.timeline ?? [];
				const last = items[items.length - 1];
				if (last && last.kind === 'reasoning' && !last.done) {
					// Still the same reasoning pass — append to it in place
					// rather than spawning a new timeline item per chunk.
					last.content += e.content;
					turn.timeline = [...items];
				} else {
					turn.timeline = [...items, { kind: 'reasoning', content: e.content, done: false }];
				}
				break;
			}

			case 'tool_call':
				this.closeOpenReasoning(turn);
				turn.timeline = [
					...(turn.timeline ?? []),
					{ kind: 'tool', tool: e.tool, args: e.args, done: false }
				];
				break;

			case 'tool_result': {
				const items = [...(turn.timeline ?? [])];
				for (let i = items.length - 1; i >= 0; i--) {
					const item = items[i];
					if (item.kind === 'tool' && item.tool === e.tool && !item.done) {
						items[i] = { ...item, result: e.result, provider: e.provider, citations: e.citations, done: true };
						break;
					}
				}
				turn.timeline = items;
				break;
			}

			case 'compacted':
				this.closeOpenReasoning(turn);
				turn.timeline = [...(turn.timeline ?? []), { kind: 'compacted', summary: e.content }];
				break;

			case 'commentary':
				this.closeOpenReasoning(turn);
				// Whatever just streamed in live via 'token' for this turn
				// was this commentary, not the final answer — the server
				// sends the same text again here as the authoritative
				// version once it knows that for certain (see
				// gateway/protocol.go's doc comment on this event). Drop
				// the flat accumulation and show it as its own timeline
				// item instead, positioned exactly where it happened
				// relative to the tool calls before/after it, rather than
				// letting it silently pile into the real final answer.
				turn.content = '';
				turn.timeline = [...(turn.timeline ?? []), { kind: 'commentary', content: e.content }];
				break;

			case 'token':
				this.closeOpenReasoning(turn);
				// e.content can be absent (not just empty) — ServerEvent's
				// omitempty JSON tag drops the field entirely for an empty
				// string, so a plain `turn.content += e.content` would
				// string-concatenate the literal text "undefined" here.
				turn.content += e.content ?? '';
				break;

			case 'done': {
				this.closeOpenReasoning(turn);
				turn.streaming = false;
				turn.citations = e.citations;
				turn.cards = e.cards;
				turn.costUsd = e.cost_usd ?? 0;
				turn.durationMs = e.duration_ms;
				this.busy = false;
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
					pendingAbandoned: this.pendingAbandoned,
					currentThreadId: this.currentThreadId,
					pendingThreadId: this.pendingThreadId,
					eventThreadId: e.thread_id
				});
				if (stillWatching) {
					this.currentThreadId = e.thread_id;
					// ?? 0 guards against a missing cost_usd (e.g. an older
					// cached frontend bundle talking to a newer backend, or
					// vice versa) turning totalCost into a sticky NaN that
					// poisons every subsequent addition for the rest of the
					// session — this exact bug shipped once already.
					this.totalCost += e.cost_usd ?? 0;
					if (e.context_tokens !== undefined) this.contextTokens = e.context_tokens;
					// Cleared here, not filled in — follow-up suggestions are
					// a separate LLM call the backend now runs after "done"
					// ships (see protocol.go's doc comment on the "suggestions"
					// event type) precisely so the turn footer doesn't stall
					// waiting on them. They arrive moments later via the
					// 'suggestions' case below and render underneath the
					// footer that's already visible.
					this.suggestions = [];
					// An edit/retry that just finished may have forked a new
					// variant into existence — ServerEvent carries no
					// variants field (openThread/swapVariant are the only
					// other places appState.variants gets set), so without
					// this the switcher stayed invisible until the thread
					// was closed and reopened, even though the fork existed
					// correctly server-side the whole time. Harmless no-op
					// for a plain send: the variants map just comes back
					// the same as before.
					void this.refreshVariants(e.thread_id);
				}
				this.pendingTurn = null;
				this.pendingUserTurn = null;
				this.pendingThreadId = null;
				void this.loadThreads();
				// Retries a version-change reload checkVersion() deferred
				// while this turn was in flight (see its doc comment) —
				// without this, a build that landed mid-turn wouldn't be
				// noticed again until the next 30s poll happens to land.
				void this.checkVersion();
				break;
			}

			case 'error':
				this.closeOpenReasoning(turn);
				turn.streaming = false;
				if (!turn.content) turn.content = `Error: ${e.message}`;
				this.busy = false;
				this.pendingTurn = null;
				this.pendingUserTurn = null;
				this.pendingThreadId = null;
				void this.checkVersion();
				break;
		}
	}
}

export const appState = new AppState();
