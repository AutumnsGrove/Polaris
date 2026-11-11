import type { ClientMessage, ServerEvent } from './types';

// How often the sidebar's connection trace is fed a beat. This number IS the
// visual rhythm: a beat is born every HEARTBEAT_MS, and each one takes
// BEAT_TRAVEL_MS to cross the box (Sidebar.svelte's @keyframes beat-travel) —
// so travel/interval is how many beats are on screen at once (6s / 2s = 3).
// Changing one without the other silently changes that count, which was
// picked deliberately: see mockups/heartbeat-sweep-spacing.html.
const HEARTBEAT_MS = 2000;

export class AgentSocket {
	private ws: WebSocket | null = null;
	private onEvent: (e: ServerEvent) => void;
	private onStatusChange: (connected: boolean) => void;
	private onReconnect: () => void;
	private onBeat: () => void;
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
	private hasConnectedBefore = false;

	constructor(
		onEvent: (e: ServerEvent) => void,
		onStatusChange: (connected: boolean) => void,
		onReconnect: () => void,
		// Called once per 'pong' — i.e. once per ping the server actually
		// answered. Deliberately a callback rather than the socket owning the
		// beat list: the trace lives in the sidebar, and the socket stays a
		// transport with no UI state of its own.
		onBeat: () => void
	) {
		this.onEvent = onEvent;
		this.onStatusChange = onStatusChange;
		this.onReconnect = onReconnect;
		this.onBeat = onBeat;
	}

	connect() {
		const proto = location.protocol === 'https:' ? 'wss' : 'ws';
		this.ws = new WebSocket(`${proto}://${location.host}/ws`);

		this.ws.onopen = () => {
			this.onStatusChange(true);
			this.startHeartbeat();
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
			this.stopHeartbeat();
			this.onStatusChange(false);
			// Retry — the potato/dev server may just be restarting after `update`.
			this.reconnectTimer = setTimeout(() => this.connect(), 2000);
		};
		this.ws.onmessage = (ev) => {
			try {
				const event = JSON.parse(ev.data) as ServerEvent;
				// The heartbeat's own answer, consumed here so the app's event
				// switch never has to know about it (it's not a turn event and
				// carries no thread_id).
				if (event.type === 'pong') {
					this.onBeat();
					return;
				}
				this.onEvent(event);
			} catch {
				// ignore malformed frames
			}
		};
	}

	// Pings only while the socket is open. A missing pong shows up on its own:
	// no beat means no ping came back, which is exactly what the trace should
	// say, so there's no separate "missed heartbeat" timeout to maintain.
	private startHeartbeat() {
		this.stopHeartbeat();
		this.heartbeatTimer = setInterval(() => {
			if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify({ type: 'ping' }));
		}, HEARTBEAT_MS);
	}

	private stopHeartbeat() {
		if (this.heartbeatTimer) clearInterval(this.heartbeatTimer);
		this.heartbeatTimer = null;
	}

	send(msg: ClientMessage) {
		if (this.ws?.readyState === WebSocket.OPEN) {
			this.ws.send(JSON.stringify(msg));
		}
	}

	close() {
		if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
		this.stopHeartbeat();
		this.ws?.close();
	}
}
