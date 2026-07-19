import type { ChatTurn, ModelOption, Thread, ServerEvent, Citation } from './types';
import { AgentSocket } from './ws';
import { synthesize } from './speech';

function safeParseJSON<T>(json: string): T[] {
	try {
		return JSON.parse(json) ?? [];
	} catch {
		return [];
	}
}

class AppState {
	turns = $state<ChatTurn[]>([]);
	threads = $state<Thread[]>([]);
	models = $state<ModelOption[]>([]);
	selectedModel = $state<string>('');
	currentThreadId = $state<string | null>(null);
	connected = $state(false);
	busy = $state(false);
	totalCost = $state(0);

	// Context-usage display, next to thread cost. contextWindowTokens is
	// the auto-compaction threshold from config.yaml (loaded once via
	// loadSettings); contextTokens is the current thread's last-known
	// prompt+completion size, from the LLM's own usage numbers.
	contextTokens = $state(0);
	contextWindowTokens = $state(100_000);

	// Follow-up suggestions for the most recent answer — persisted on the
	// last assistant message (see StoredMessage.suggestions), so openThread
	// restores them; cleared on new dispatch/new thread since there's no
	// "most recent answer" yet at that point.
	suggestions = $state<string[]>([]);

	// Desktop: sidebar sits inline, open by default. Mobile: it's an
	// overlay drawer, closed by default so the chat is visible first.
	// +layout.svelte sets the initial value from viewport width on mount.
	sidebarOpen = $state(true);

	settingsOpen = $state(false);
	theme = $state<'dark' | 'light'>('dark');
	showPrices = $state(true);
	defaultModel = $state('');

	// Per-turn read-aloud is manual (see readAloud below). speakingIndex is
	// set the instant synthesis starts (fetching); isPlaying flips true
	// only once audio actually starts playing — the button needs both to
	// distinguish "loading" from "playing, click to stop" from "idle".
	// A future full "voice mode" session (auto-speak every reply, a
	// brief-answer prompt hint) can build on the same plumbing later.
	speakingIndex = $state<number | null>(null);
	isPlaying = $state(false);
	private currentAudio: HTMLAudioElement | null = null;

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
	}

	toggleSidebar() {
		this.sidebarOpen = !this.sidebarOpen;
	}

	closeSidebar() {
		this.sidebarOpen = false;
	}

	// Manual per-message read-aloud, triggered from the speaker icon next
	// to a turn's retry button. Clicking the turn that's already active
	// (loading OR playing) stops it — a toggle, not just a one-way trigger.
	async readAloud(assistantTurnIndex: number) {
		if (this.speakingIndex === assistantTurnIndex) {
			this.stopReadAloud();
			return;
		}

		const turn = this.turns[assistantTurnIndex];
		if (!turn || turn.role !== 'assistant' || !turn.content) return;

		this.stopReadAloud(); // only one read-aloud plays at a time
		this.speakingIndex = assistantTurnIndex;

		const result = await synthesize(turn.content, this.currentThreadId ?? undefined);
		if (!result) {
			if (this.speakingIndex === assistantTurnIndex) this.speakingIndex = null;
			return;
		}
		// Stopped (or a different turn started) while we were still fetching.
		if (this.speakingIndex !== assistantTurnIndex) return;

		if (result.cost) this.totalCost += result.cost;
		this.currentAudio = result.audio;
		result.audio.onended = () => {
			if (this.currentAudio === result.audio) {
				this.currentAudio = null;
				this.isPlaying = false;
				this.speakingIndex = null;
			}
		};

		try {
			await result.audio.play();
			this.isPlaying = true;
		} catch (err) {
			console.error('audio playback failed', err);
			this.speakingIndex = null;
		}
	}

	stopReadAloud() {
		if (this.currentAudio) {
			this.currentAudio.onended = null;
			this.currentAudio.pause();
			this.currentAudio = null;
		}
		this.isPlaying = false;
		this.speakingIndex = null;
	}

	async loadModels() {
		const res = await fetch('/api/models');
		this.models = (await res.json()) ?? [];
		const def = this.models.find((m) => m.default);
		this.selectedModel = def?.id ?? this.models[0]?.id ?? '';
	}

	async loadSettings() {
		const res = await fetch('/api/settings');
		if (!res.ok) return;
		const data = await res.json();
		this.theme = data.theme === 'light' ? 'light' : 'dark';
		this.showPrices = data.show_prices ?? true;
		this.defaultModel = data.default_model ?? '';
		this.contextWindowTokens = data.context_window_tokens ?? 100_000;
		this.applyTheme();
	}

	private applyTheme() {
		if (typeof document !== 'undefined') {
			document.documentElement.setAttribute('data-theme', this.theme);
		}
	}

	async setTheme(theme: 'dark' | 'light') {
		this.theme = theme;
		this.applyTheme();
		await this.putSettings({ theme });
	}

	async setShowPrices(show: boolean) {
		this.showPrices = show;
		await this.putSettings({ show_prices: show });
	}

	async setDefaultModel(modelId: string) {
		this.defaultModel = modelId;
		await this.putSettings({ default_model: modelId });
		await this.loadModels();
	}

	private async putSettings(body: Record<string, unknown>) {
		await fetch('/api/settings', {
			method: 'PUT',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(body)
		});
	}

	toggleSettings() {
		this.settingsOpen = !this.settingsOpen;
	}

	/** Runs the same git-pull-and-rebuild the CLI's `polaris update` does. */
	async pushUpdate(): Promise<{ success: boolean; log: string; restarting?: boolean; error?: string }> {
		const res = await fetch('/api/update', { method: 'POST' });
		return res.json();
	}

	async loadThreads() {
		const res = await fetch('/api/threads');
		this.threads = (await res.json()) ?? [];
	}

	async openThread(id: string) {
		const res = await fetch(`/api/threads/${id}`);
		if (!res.ok) return;
		const data = await res.json();
		this.currentThreadId = id;
		this.totalCost = data.cost_usd ?? 0;
		this.contextTokens = data.context_tokens ?? 0;
		const messages = data.messages ?? [];
		this.turns = messages.map((m: any) => ({
			role: m.role,
			content: m.content,
			citations: safeParseJSON<Citation>(m.citations),
			costUsd: m.cost_usd,
			id: m.role === 'user' ? m.id : undefined
		}));
		// Suggestions are a "what's next" prompt for the last answer, so
		// only the most recent assistant message's set is relevant here.
		const lastAssistant = [...messages].reverse().find((m: any) => m.role === 'assistant');
		this.suggestions = lastAssistant ? safeParseJSON<string>(lastAssistant.suggestions) : [];
		this.closeSidebarIfMobile();
	}

	newThread() {
		this.currentThreadId = null;
		this.turns = [];
		this.totalCost = 0;
		this.contextTokens = 0;
		this.suggestions = [];
		this.closeSidebarIfMobile();
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

	// Cancels the in-flight turn. The backend aborts its LLM/tool calls
	// mid-flight and still sends a normal 'done' with whatever streamed so
	// far — no separate "stopped" event type needed, the existing done
	// handler already finalizes the turn correctly.
	stopGeneration() {
		if (!this.busy) return;
		this.socket.send({ type: 'stop' });
	}

	// sttCostUsd is set when content came from a transcribed voice memo
	// (already billed via /api/transcribe) so it gets folded into the
	// thread's running total instead of silently untracked.
	send(content: string, sttCostUsd?: number) {
		const trimmed = content.trim();
		if (!trimmed || this.busy) return;
		this.dispatch(trimmed, undefined, undefined, sttCostUsd);
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
	private dispatch(content: string, editFromId?: number, truncateFromIndex?: number, sttCostUsd?: number) {
		if (truncateFromIndex !== undefined) {
			this.turns = this.turns.slice(0, truncateFromIndex);
		}
		this.suggestions = [];
		this.turns.push({ role: 'user', content });
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

		this.socket.send({
			type: 'message',
			thread_id: this.currentThreadId ?? undefined,
			content,
			model: this.selectedModel,
			edit_from_id: editFromId,
			stt_cost_usd: sttCostUsd
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
		if (this.pendingIsNewThread && this.pendingThreadId === null && eventThreadId) {
			this.pendingThreadId = eventThreadId;
		}

		// Not for the turn we're tracking — most likely a late event for a
		// turn the user has since navigated away from. The backend is
		// still persisting it independently regardless; reopening that
		// thread later will show the finished result. Just don't let it
		// touch whatever's currently on screen.
		if (eventThreadId && eventThreadId !== this.pendingThreadId) return;

		if (e.type === 'user_message') {
			if (this.pendingUserTurn) this.pendingUserTurn.id = e.user_message_id;
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
						items[i] = { ...item, result: e.result, citations: e.citations, done: true };
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

			case 'token':
				this.closeOpenReasoning(turn);
				turn.content += e.content;
				break;

			case 'done': {
				this.closeOpenReasoning(turn);
				turn.streaming = false;
				turn.citations = e.citations;
				turn.costUsd = e.cost_usd;
				this.busy = false;
				// Only adopt the thread id / bump the visible total if the
				// user is still looking at this thread (or it just became
				// one) — not if they've since navigated elsewhere.
				const stillWatching = this.currentThreadId === null || this.currentThreadId === this.pendingThreadId;
				if (stillWatching) {
					this.currentThreadId = e.thread_id;
					this.totalCost += e.cost_usd;
					if (e.context_tokens !== undefined) this.contextTokens = e.context_tokens;
					this.suggestions = e.suggestions ?? [];
				}
				this.pendingTurn = null;
				this.pendingUserTurn = null;
				this.pendingThreadId = null;
				void this.loadThreads();
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
				break;
		}
	}
}

export const appState = new AppState();
