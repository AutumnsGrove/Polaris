import type { PendingQuestion, WizardFinal, WizardResponse, WizardTarget } from './types';

// One transcript entry — either side of the exchange, or a drafted-text
// card rendered inline (see WizardOverlay.svelte). Kept separate from
// PendingQuestion/WizardFinal themselves so the transcript can hold more
// than one of each across a multi-round interview.
export type WizardTranscriptEntry =
	| { kind: 'user'; text: string }
	| { kind: 'question'; question: PendingQuestion }
	| { kind: 'final'; final: WizardFinal }
	// Fallback for a plain-prose reply with no tool call — see
	// gateway/wizard.go's wizardResponse doc comment. Rare (the
	// system prompt asks the model to always call a tool) but a real,
	// observed case: without this, that reply had nowhere to render and
	// the wizard looked frozen after a tap.
	| { kind: 'text'; text: string };

// WizardState drives the "help me write this" floating overlay for every
// surface that offers it (Pulsar routine prompts, Pulsar Daily blocks, Field
// instructions) — see gateway/wizard.go. One shared class, not one per
// surface: the interview is identical, only what it's writing differs, and
// that's just the WizardTarget passed to start(). Deliberately its own
// small class, not folded into PulsarState or FieldsState: the wizard's
// session is ephemeral (server-side state discarded on session expiry,
// client-side state discarded on close), with nothing in common with any
// surface's persisted data.
export class WizardState {
	open = $state(false);
	sessionId = $state<string | null>(null);
	transcript = $state<WizardTranscriptEntry[]>([]);
	pendingQuestion = $state<PendingQuestion | null>(null);
	loading = $state(false);
	error = $state('');

	// start() seeds the interview with whatever's currently typed into the
	// calling form's field, if anything — an empty seed opens with that
	// target's own generic opener question instead (its opener_task in
	// prompts.yaml's wizard.targets). target picks which interview this is;
	// the server rejects an unknown kind with a 400, surfaced as `error`.
	async start(target: WizardTarget, seed: string) {
		this.open = true;
		this.sessionId = null;
		this.transcript = [];
		this.pendingQuestion = null;
		this.error = '';
		this.loading = true;
		try {
			const res = await fetch('/api/wizard/start', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ target: target.kind, label: target.label ?? '', seed })
			});
			if (!res.ok) {
				this.error = (await res.text()) || 'Something went wrong starting the wizard.';
				return;
			}
			const data = (await res.json()) as WizardResponse;
			this.sessionId = data.session_id;
			this.applyResponse(data);
		} catch {
			this.error = 'Could not reach the server — try again.';
		} finally {
			this.loading = false;
		}
	}

	// answer() is both "reply to the current question" and "send a
	// free-text follow-up after a drafted prompt appeared" — the wizard
	// compose box stays live the whole time (see
	// WizardOverlay.svelte), so there's no separate code path for
	// refining after finalize_wizard_prompt already fired once.
	async answer(text: string) {
		const trimmed = text.trim();
		if (!trimmed || !this.sessionId || this.loading) return;
		this.transcript = [...this.transcript, { kind: 'user', text: trimmed }];
		this.pendingQuestion = null;
		this.error = '';
		this.loading = true;
		try {
			const res = await fetch('/api/wizard/turn', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ session_id: this.sessionId, message: trimmed })
			});
			if (!res.ok) {
				this.error =
					res.status === 410
						? 'This session timed out — close and try again.'
						: (await res.text()) || 'Something went wrong.';
				return;
			}
			this.applyResponse((await res.json()) as WizardResponse);
		} catch {
			this.error = 'Could not reach the server — try again.';
		} finally {
			this.loading = false;
		}
	}

	private applyResponse(data: WizardResponse) {
		if (data.question) {
			this.pendingQuestion = data.question;
			this.transcript = [...this.transcript, { kind: 'question', question: data.question }];
		} else if (data.final) {
			this.transcript = [...this.transcript, { kind: 'final', final: data.final }];
		} else if (data.answer) {
			this.transcript = [...this.transcript, { kind: 'text', text: data.answer }];
		}
	}

	// close() discards the whole session, client-side — the server-side
	// copy is left to expire on its own (wizardSessionTTL), no explicit
	// delete call, since there's nothing sensitive in it worth an extra
	// round trip to clean up early. Matches the confirmed UX: closing for
	// any reason (done, cancel, backdrop click) throws away the exchange;
	// only an accepted prompt (copied out via onAccept) survives.
	close() {
		this.open = false;
		this.sessionId = null;
		this.transcript = [];
		this.pendingQuestion = null;
		this.error = '';
		this.loading = false;
	}
}

export const wizardState = new WizardState();
