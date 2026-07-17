import type { ClientMessage, ServerEvent } from './types';

// Thin wrapper around the raw WebSocket — reconnect logic lives here so
// state.svelte.ts doesn't have to think about connection lifecycle.
export class AgentSocket {
	private ws: WebSocket | null = null;
	private onEvent: (e: ServerEvent) => void;
	private onStatusChange: (connected: boolean) => void;
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

	constructor(onEvent: (e: ServerEvent) => void, onStatusChange: (connected: boolean) => void) {
		this.onEvent = onEvent;
		this.onStatusChange = onStatusChange;
	}

	connect() {
		const proto = location.protocol === 'https:' ? 'wss' : 'ws';
		this.ws = new WebSocket(`${proto}://${location.host}/ws`);

		this.ws.onopen = () => this.onStatusChange(true);
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
