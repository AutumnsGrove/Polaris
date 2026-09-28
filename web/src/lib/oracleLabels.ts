import { FOCUS_MODES } from './focusModes';
import type { OracleResult } from './types';

// Human labels for the fixed, small option vocabularies prompts.yaml's
// oracle.checks.high_stakes/intent define — mirrors FOCUS_MODES' own
// id -> label map below, just for the two checks whose fired option
// becomes the margin note's "Read as X" clause. Kept here (not
// prompts.yaml) since these are purely a display concern; the actual
// classification options are the server's source of truth.
const HIGH_STAKES_LABELS: Record<string, string> = {
	medical: 'medical',
	legal: 'legal',
	financial: 'financial',
	safety: 'a safety question'
};

const INTENT_LABELS: Record<string, string> = {
	place: 'a place',
	book: 'a book',
	film_tv: 'a movie or show',
	music: 'music',
	product: 'a product',
	weather: 'weather',
	video: 'a video',
	code: 'code',
	definition: 'a word'
};

function focusLabel(mode: string): string {
	return FOCUS_MODES.find((m) => m.id === mode)?.label ?? mode;
}

// True only for the F1 "mid-thread switch" case (mockups/oracle-mode.html)
// — Oracle's own pick actually took effect this turn AND it differs from
// the nearest earlier turn's own applied mode. Shared by buildOracleNote's
// text (below) and ChatTurnView.svelte's tap-to-undo/one-time-glow
// behavior, so the two can't drift out of sync on what counts as "a real
// switch" vs. Oracle just re-picking the same mode it already had.
export function focusSwitch(
	oracleFocusModeSource: string | undefined,
	appliedFocusMode: string | undefined,
	previousAppliedFocusMode: string | undefined
): { from: string; to: string } | null {
	if (
		oracleFocusModeSource === 'oracle' &&
		appliedFocusMode &&
		previousAppliedFocusMode &&
		previousAppliedFocusMode !== appliedFocusMode
	) {
		return { from: previousAppliedFocusMode, to: appliedFocusMode };
	}
	return null;
}

// Builds the margin note's text — see ChatTurnView.svelte's rendering and
// mockups/oracle-mode.html's B2/F1 examples ("Read as medical · answered as
// Researcher", "Ambiguous · asking first", "Refers to a past chat",
// "Switched Shopper -> First Principles"). Returns null when nothing fired
// that's worth a note (a real, silent "Oracle ran but had nothing to say"
// outcome, not an error).
//
// appliedFocusMode/previousAppliedFocusMode are this turn's and the
// nearest earlier assistant turn's own store.Message.AppliedFocusMode
// (ChatTurnView.svelte looks the latter up by scanning appState.turns
// backwards) — see gateway/protocol.go's ServerEvent.AppliedFocusMode doc
// comment for why this, not oracleResult.focus_mode alone, is what can
// distinguish "kept your X" (a manual/default pick still in effect) from
// "Switched X -> Y" (Oracle just changed it) from a plain "answered as X"
// (the first time in this thread, nothing to compare against).
export function buildOracleNote(
	oracleResult: OracleResult | undefined,
	oracleFocusModeSource: string | undefined,
	appliedFocusMode: string | undefined,
	previousAppliedFocusMode: string | undefined
): string | null {
	if (!oracleResult) return null;

	const clarify = oracleResult.checks?.find((c) => c.key === 'clarify');
	if (clarify?.fired && clarify.winner === 'yes') {
		return '<b>Ambiguous</b> · asking first';
	}

	const highStakes = oracleResult.checks?.find((c) => c.key === 'high_stakes');
	const intent = oracleResult.checks?.find((c) => c.key === 'intent');
	const readAs =
		(highStakes?.fired && highStakes.winner !== 'none' ? HIGH_STAKES_LABELS[highStakes.winner] : undefined) ??
		(intent?.fired && intent.winner !== 'general' ? INTENT_LABELS[intent.winner] : undefined);

	let focusClause: string | undefined;
	if (appliedFocusMode) {
		if (oracleFocusModeSource === 'oracle') {
			const sw = focusSwitch(oracleFocusModeSource, appliedFocusMode, previousAppliedFocusMode);
			if (sw) {
				focusClause = `Switched <b class="old">${focusLabel(sw.from)}</b> → <b>${focusLabel(sw.to)}</b>`;
			} else {
				// First time in the thread, or Oracle re-picking the same mode
				// it already had — either way, not a real "switch" worth
				// calling out.
				focusClause = `answered as <b>${focusLabel(appliedFocusMode)}</b>`;
			}
		} else {
			// "manual" or "default" — the operator's own pick (or the
			// standing default) is what's in effect, not anything Oracle
			// chose; "kept" reads correctly whether or not Oracle even
			// looked at focus this turn.
			focusClause = `kept your <b>${focusLabel(appliedFocusMode)}</b>`;
		}
	}

	if (readAs && focusClause) return `Read as <b>${readAs}</b> · ${focusClause}`;
	if (readAs) return `Read as <b>${readAs}</b>`;
	if (focusClause) return focusClause.charAt(0).toUpperCase() + focusClause.slice(1);

	const recall = oracleResult.checks?.find((c) => c.key === 'recall');
	if (recall?.fired && recall.winner === 'yes') return 'Refers to <b>a past chat</b>';

	return null;
}

// TurnInfoSheet's own display config — one row per check prompts.yaml's
// oracle.checks/chips define, in the order the sheet lists them. Kept as
// a fixed, ordered list (not derived from whatever keys happen to appear
// in a given turn's oracleResult.checks) so the sheet's layout doesn't
// reflow turn to turn, and so a check that simply didn't run this turn
// (budget/timeout skipped everything past it) still has a defined name
// rather than falling back to its raw key.
export const CHECK_DISPLAY: { key: string; name: string }[] = [
	{ key: 'focus', name: 'Focus' },
	{ key: 'high_stakes', name: 'High stakes' },
	{ key: 'intent', name: 'Topic' },
	{ key: 'research', name: 'Research' },
	{ key: 'clarify', name: 'Clarify first' },
	{ key: 'recall', name: 'Past chats' }
];

// Per-check option -> short display label, for the sheet's option-odds
// bars and the quiet-list's one-line summary. Falls back to the raw
// option string (capitalized) for anything not listed here, so a
// prompts.yaml addition doesn't render as literally blank.
const OPTION_LABELS: Record<string, Record<string, string>> = {
	focus: {
		off: 'Off',
		brief: 'Brief',
		researcher: 'Researcher',
		academic: 'Academic',
		news: 'News',
		shopper: 'Shopper',
		first_principles: 'First Principles',
		socratic: 'Socratic',
		safari: 'Safari'
	},
	high_stakes: {
		none: 'None',
		medical: 'Medical',
		legal: 'Legal',
		financial: 'Financial',
		safety: 'Safety'
	},
	intent: {
		general: 'General',
		place: 'Place',
		book: 'Books',
		film_tv: 'Film/TV',
		music: 'Music',
		product: 'Product',
		weather: 'Weather',
		video: 'Video',
		code: 'Code',
		definition: 'Definition'
	},
	research: { yes: 'Needed', no: 'Not needed' },
	clarify: { yes: 'Yes', no: 'No' },
	recall: { yes: 'Yes', no: 'No' }
};

export function optionLabel(checkKey: string, option: string): string {
	return OPTION_LABELS[checkKey]?.[option] ?? option.charAt(0).toUpperCase() + option.slice(1);
}

// The check-state pill's text — "Set"/"Not set" for focus (a mode pick,
// not a nudge), "Nudged"/"Quiet" for every other check (it either
// injected guidance into this turn's system prompt or it didn't).
export function checkStateLabel(checkKey: string, fired: boolean): string {
	if (checkKey === 'focus') return fired ? 'Set' : 'Not set';
	return fired ? 'Nudged' : 'Quiet';
}

// Offer-chip key -> display name, for TurnInfoSheet's quiet-list "Offers"
// row — see gateway/oracle.go's Chip doc comment. "project" carries its
// own Label (the project name) rather than a fixed name here.
export const CHIP_NAMES: Record<string, string> = {
	pulsar: 'Pulsar',
	daily: 'Daily'
};
