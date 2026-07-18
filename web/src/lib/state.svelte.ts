import type { ChatTurn, ModelOption, Thread, ServerEvent, Citation } from './types';
import { AgentSocket } from './ws';

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
	}

	newThread() {
		this.currentThreadId = null;
		this.turns = [];
		this.totalCost = 0;
	}

	async deleteThread(id: string) {
		await fetch(`/api/threads/${id}`, { method: 'DELETE' });
		if (this.currentThreadId === id) this.newThread();
		await this.loadThreads();
	}

	send(content: string) {
		const trimmed = content.trim();
		if (!trimmed || this.busy) return;
		this.dispatch(trimmed);
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
	private dispatch(content: string, editFromId?: number, truncateFromIndex?: number) {
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
			edit_from_id: editFromId
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
