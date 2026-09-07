import type { PendingQuestion, WizardFinal, WizardResponse } from './types';

// One transcript entry — either side of the exchange, or a drafted-prompt
// card rendered inline (see PulsarPromptWizard.svelte). Kept separate from
// PendingQuestion/WizardFinal themselves so the transcript can hold more
// than one of each across a multi-round interview.
export type WizardTranscriptEntry =
	| { kind: 'user'; text: string }
	| { kind: 'question'; question: PendingQuestion }
	| { kind: 'final'; final: WizardFinal }
	// Fallback for a plain-prose reply with no tool call — see
	// gateway/pulsar_wizard.go's wizardResponse doc comment. Rare (the
	// system prompt asks the model to always call a tool) but a real,
	// observed case: without this, that reply had nowhere to render and
	// the wizard looked frozen after a tap.
	| { kind: 'text'; text: string };

// PulsarWizardState drives the "help me write the prompt" floating
// overlay — see docs/plans/pulsar-routines.md's v1.2 note and
// gateway/pulsar_wizard.go. Deliberately its own small class, not folded
// into PulsarState: the wizard's session is ephemeral (server-side state
// discarded on session expiry, client-side state discarded on close),
// with nothing in common with routines/pulses' persisted data.
export class PulsarWizardState {
	open = $state(false);
	sessionId = $state<string | null>(null);
	transcript = $state<WizardTranscriptEntry[]>([]);
	pendingQuestion = $state<PendingQuestion | null>(null);
	loading = $state(false);
	error = $state('');

	// start() seeds the interview with whatever's currently typed into the
	// routine form's prompt field, if anything — an empty seed opens with
	// the backend's generic opener question instead (see
	// prompts.PulsarWizard.OpenerTask). dailyBlockTitle, when set, scopes
	// the whole interview to writing a short steering instruction for one
	// Pulsar Daily block instead of a routine prompt — see
	// gateway/pulsar_wizard.go's wizardStartRequest.DailyBlockTitle.
	// isCustomBlock, only meaningful alongside dailyBlockTitle, further
	// scopes it to a custom block's own full instructions field instead of
	// a fixed block's one-line steer — see wizardStartRequest.
	// IsCustomDailyBlock.
	async start(seed: string, dailyBlockTitle?: string, isCustomBlock?: boolean) {
		this.open = true;
		this.sessionId = null;
		this.transcript = [];
		this.pendingQuestion = null;
		this.error = '';
		this.loading = true;
		try {
			const res = await fetch('/api/pulsar/wizard/start', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					seed,
					daily_block_title: dailyBlockTitle ?? '',
					is_custom_daily_block: isCustomBlock ?? false
				})
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
	// PulsarPromptWizard.svelte), so there's no separate code path for
	// refining after finalize_pulsar_prompt already fired once.
	async answer(text: string) {
		const trimmed = text.trim();
		if (!trimmed || !this.sessionId || this.loading) return;
		this.transcript = [...this.transcript, { kind: 'user', text: trimmed }];
		this.pendingQuestion = null;
		this.error = '';
		this.loading = true;
		try {
			const res = await fetch('/api/pulsar/wizard/turn', {
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

export const pulsarWizardState = new PulsarWizardState();
