import type { ClientMessage, ServerEvent } from './types';

// Thin wrapper around the raw WebSocket — reconnect logic lives here so
// state.svelte.ts doesn't have to think about connection lifecycle.
export class AgentSocket {
	private ws: WebSocket | null = null;
	private onEvent: (e: ServerEvent) => void;
	private onStatusChange: (connected: boolean) => void;
	private onReconnect: () => void;
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private hasConnectedBefore = false;

	constructor(
		onEvent: (e: ServerEvent) => void,
		onStatusChange: (connected: boolean) => void,
		onReconnect: () => void
	) {
		this.onEvent = onEvent;
		this.onStatusChange = onStatusChange;
		this.onReconnect = onReconnect;
	}

	connect() {
		const proto = location.protocol === 'https:' ? 'wss' : 'ws';
		this.ws = new WebSocket(`${proto}://${location.host}/ws`);

		this.ws.onopen = () => {
			this.onStatusChange(true);
			// A *re*-connect (not the very first connect of this session)
			// means any in-flight turn's events are gone for good — mobile
			// browsers routinely kill background-tab WebSockets, and the
			// server has no way to redeliver events to a brand-new socket.
			// The backend kept working and persisted its result regardless;
			// the frontend just needs to go re-fetch it instead of sitting
			// on a stream that will never resume.
			if (this.hasConnectedBefore) this.onReconnect();
			this.hasConnectedBefore = true;
		};
		this.ws.onclose = () => {
			this.onStatusChange(false);
			// Retry — the potato/dev server may just be restarting after `update`.
			this.reconnectTimer = setTimeout(() => this.connect(), 2000);
		};
		this.ws.onmessage = (ev) => {
			try {
				this.onEvent(JSON.parse(ev.data) as ServerEvent);
			} catch {
				// ignore malformed frames
			}
		};
	}

	send(msg: ClientMessage) {
		if (this.ws?.readyState === WebSocket.OPEN) {
			this.ws.send(JSON.stringify(msg));
		}
	}

	close() {
		if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
		this.ws?.close();
	}
}
