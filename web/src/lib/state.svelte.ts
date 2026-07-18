import type { ChatTurn, ModelOption, Thread, ServerEvent, Citation } from './types';
import { AgentSocket } from './ws';
import { synthesize } from './speech';

function safeParseCitations(json: string): Citation[] {
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

	// Desktop: sidebar sits inline, open by default. Mobile: it's an
	// overlay drawer, closed by default so the chat is visible first.
	// +layout.svelte sets the initial value from viewport width on mount.
	sidebarOpen = $state(true);

	// Per-turn read-aloud is manual (see readAloud below). speakingIndex is
	// set the instant synthesis starts (fetching); isPlaying flips true
	// only once audio actually starts playing — the button needs both to
	// distinguish "loading" from "playing, click to stop" from "idle".
	// A future full "voice mode" session (auto-speak every reply, a
	// brief-answer prompt hint) can build on the same plumbing later.
	speakingIndex = $state<number | null>(null);
	isPlaying = $state(false);
	private currentAudio: HTMLAudioElement | null = null;

	private socket: AgentSocket;

	constructor() {
		this.socket = new AgentSocket(
			(e) => this.handleEvent(e),
			(connected) => (this.connected = connected)
		);
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
		this.turns = (data.messages ?? []).map((m: any) => ({
			role: m.role,
			content: m.content,
			citations: safeParseCitations(m.citations),
			costUsd: m.cost_usd,
			id: m.role === 'user' ? m.id : undefined
		}));
		this.closeSidebarIfMobile();
	}

	newThread() {
		this.currentThreadId = null;
		this.turns = [];
		this.totalCost = 0;
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
		this.turns.push({ role: 'user', content });
		this.turns.push({ role: 'assistant', content: '', timeline: [], streaming: true });
		this.busy = true;

		this.socket.send({
			type: 'message',
			thread_id: this.currentThreadId ?? undefined,
			content,
			model: this.selectedModel,
			edit_from_id: editFromId,
			stt_cost_usd: sttCostUsd
		});
	}

	private currentAssistantTurn(): ChatTurn | undefined {
		return this.turns[this.turns.length - 1];
	}

	private currentUserTurn(): ChatTurn | undefined {
		return this.turns[this.turns.length - 2];
	}

	private handleEvent(e: ServerEvent) {
		if (e.type === 'user_message') {
			const userTurn = this.currentUserTurn();
			if (userTurn && userTurn.role === 'user') userTurn.id = e.user_message_id;
			return;
		}

		const turn = this.currentAssistantTurn();
		if (!turn) return;

		switch (e.type) {
			case 'thinking':
				turn.timeline = [...(turn.timeline ?? []), { kind: 'thinking', content: e.content }];
				break;

			case 'tool_call':
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

			case 'token':
				turn.content += e.content;
				break;

			case 'done':
				turn.streaming = false;
				turn.citations = e.citations;
				turn.costUsd = e.cost_usd;
				this.totalCost += e.cost_usd;
				this.currentThreadId = e.thread_id;
				this.busy = false;
				void this.loadThreads();
				break;

			case 'error':
				turn.streaming = false;
				if (!turn.content) turn.content = `Error: ${e.message}`;
				this.busy = false;
				break;
		}
	}
}

export const appState = new AppState();
