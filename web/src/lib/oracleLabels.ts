import { FOCUS_MODES } from './focusModes';
import { splitContent } from './uiBlocks/split';
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
	definition: 'a word',
	academic_paper: 'a research paper',
	image: 'images',
	person_org: 'a person or organization',
	recipe: 'a recipe',
	travel: 'travel',
	sports: 'sports',
	event: 'an event',
	datetime: 'the time'
};

// The task check's fired option, for the same "Read as X" clause. Only the
// options that say something about the message itself — "answer" is the
// quiet default and has no label.
const TASK_LABELS: Record<string, string> = {
	explain: 'an explanation',
	decide: 'a decision',
	plan: 'a plan',
	troubleshoot: 'a fix',
	write: 'a writing task',
	summarize: 'a summary',
	brainstorm: 'a brainstorm',
	calculate: 'a calculation'
};

// The ui check's winners, for the margin note's "shown as a <b>compare</b>
// block". Fixed and developer-authored, so safe in {@html}; a winner not
// listed (a future block kind this build doesn't know) is simply not named.
const UI_BLOCK_LABELS: Record<string, string> = {
	compare: 'compare',
	steps: 'steps',
	choose: 'decision',
	checklist: 'checklist',
	timeline: 'timeline',
	procon: 'pros and cons',
	facts: 'facts',
	flow: 'flow',
	tabs: 'tabs',
	claim: 'claim check'
};

/**
 * True when `answer` actually contains a Prism `ui` block — meaning one
 * `splitContent` hands to `UiBlocks`, not a ```ui line quoted inside a longer
 * fence. Shares the renderer's own splitter so the ⓘ note can never claim
 * "shown as a … block" for a fence that renders as inert code (or miss one the
 * renderer does show). Cheap enough to call per token: the `includes` guard
 * skips an answer with no `ui` at all.
 */
export function hasUiBlock(answer: string | undefined): boolean {
	if (!answer || !answer.includes('ui')) return false;
	return splitContent(answer, false, ['ui']).some((seg) => seg.kind === 'ui');
}

// The note and offer labels are rendered with {@html} so they can carry
// <b> emphasis; anything not from a fixed developer-authored set (an
// unrecognized focus mode id, a field name) goes through this first.
export function escapeHtml(text: string): string {
	return text
		.replaceAll('&', '&amp;')
		.replaceAll('<', '&lt;')
		.replaceAll('>', '&gt;')
		.replaceAll('"', '&quot;');
}

function focusLabel(mode: string): string {
	return FOCUS_MODES.find((m) => m.id === mode)?.label ?? escapeHtml(mode);
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
	previousAppliedFocusMode: string | undefined,
	// The turn's answer text, so the note can say "shown as a compare block"
	// only when the model actually wrote one — the ui check nudges toward a
	// block, it can't promise one.
	answer?: string
): string | null {
	if (!oracleResult) return null;

	const clarify = oracleResult.checks?.find((c) => c.key === 'clarify');
	if (clarify?.fired && clarify.winner === 'yes') {
		return '<b>Ambiguous</b> · asking first';
	}

	const highStakes = oracleResult.checks?.find((c) => c.key === 'high_stakes');
	const intent = oracleResult.checks?.find((c) => c.key === 'intent');
	const task = oracleResult.checks?.find((c) => c.key === 'task');
	const readAs =
		(highStakes?.fired && highStakes.winner !== 'none' ? HIGH_STAKES_LABELS[highStakes.winner] : undefined) ??
		(intent?.fired && intent.winner !== 'general' ? INTENT_LABELS[intent.winner] : undefined) ??
		(task?.fired ? TASK_LABELS[task.winner] : undefined);

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

	const ui = oracleResult.checks?.find((c) => c.key === 'ui');
	const uiClause =
		ui?.fired && UI_BLOCK_LABELS[ui.winner] && hasUiBlock(answer)
			? `shown as a <b>${UI_BLOCK_LABELS[ui.winner]}</b> block`
			: undefined;

	// "Read as X · answered as Y · shown as a Z block", whichever apply, in
	// that order; the first clause present is capitalized unless it already
	// starts with "Read as".
	const parts: string[] = [];
	if (readAs) parts.push(`Read as <b>${readAs}</b>`);
	if (focusClause) parts.push(focusClause);
	if (uiClause) parts.push(uiClause);
	if (parts.length) return parts[0].charAt(0).toUpperCase() + parts[0].slice(1) + parts.slice(1).map((p) => ` · ${p}`).join('');

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
	{ key: 'recall', name: 'Past chats' },
	{ key: 'task', name: 'Task' },
	{ key: 'format', name: 'Format' },
	{ key: 'ui', name: 'Visual block' },
	{ key: 'depth', name: 'Depth' },
	{ key: 'recency', name: 'Freshness' },
	{ key: 'source_type', name: 'Sources' },
	{ key: 'contested', name: 'Contested' },
	{ key: 'claim_check', name: 'Claim check' },
	{ key: 'locale', name: 'Location' },
	{ key: 'premise', name: 'Premise' },
	{ key: 'emotional', name: 'Tone' },
	{ key: 'private_person', name: 'Private person' },
	{ key: 'has_url', name: 'Link' }
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
		definition: 'Definition',
		academic_paper: 'Paper',
		image: 'Images',
		person_org: 'Person/Org',
		recipe: 'Recipe',
		travel: 'Travel',
		sports: 'Sports',
		event: 'Event',
		datetime: 'Time'
	},
	research: { yes: 'Needed', no: 'Not needed' },
	clarify: { yes: 'Yes', no: 'No' },
	recall: { yes: 'Yes', no: 'No' },
	task: {
		answer: 'Answer',
		explain: 'Explain',
		decide: 'Decide',
		plan: 'Plan',
		troubleshoot: 'Troubleshoot',
		write: 'Write',
		summarize: 'Summarize',
		brainstorm: 'Brainstorm',
		calculate: 'Calculate'
	},
	format: {
		none: 'None',
		table: 'Table',
		comparison: 'Comparison',
		steps: 'Steps',
		list: 'List',
		prose: 'Prose',
		code: 'Code',
		timeline: 'Timeline'
	},
	ui: {
		none: 'None',
		compare: 'Compare',
		steps: 'Steps',
		choose: 'Decision',
		checklist: 'Checklist',
		timeline: 'Timeline',
		procon: 'Pros and cons',
		facts: 'Facts',
		flow: 'Flow',
		tabs: 'Tabs',
		claim: 'Claim check'
	},
	depth: { standard: 'Standard', quick: 'Quick', thorough: 'Thorough' },
	recency: { evergreen: 'Evergreen', recent: 'Recent', breaking: 'Breaking' },
	source_type: {
		any: 'Any',
		primary_docs: 'Primary docs',
		community: 'Community',
		official: 'Official',
		academic: 'Academic'
	},
	contested: { yes: 'Contested', no: 'Settled' },
	claim_check: { yes: 'Claim to check', no: 'No claim' },
	locale: { yes: 'Depends on place', no: 'Universal' },
	premise: { yes: 'Loaded', no: 'Neutral' },
	emotional: { yes: 'Distressed', no: 'Neutral' },
	private_person: { yes: 'Private person', no: 'No' },
	has_url: { yes: 'Has a link' }
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
// row — see gateway/oracle.go's Chip doc comment. "field" carries its
// own Label (the field name) rather than a fixed name here.
export const CHIP_NAMES: Record<string, string> = {
	pulsar: 'Pulsar',
	daily: 'Daily',
	safari: 'Safari'
};
