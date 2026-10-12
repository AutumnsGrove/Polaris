// The parsed shape of a ```ui fence. Pure data, no DOM. The grammar lives in
// parse.ts; docs/plans/intelligent-ui.md "Grammar" and "Block catalog" are the
// spec. A discriminated union so a component can't be handed the wrong block.

export type CalloutTone = 'note' | 'warn' | 'ok' | 'answer';

export interface CompareRow {
	row: string;
	/** Always exactly `cols.length` long: padded/truncated by the parser. */
	v: string[];
	src: string[];
}

export interface StepItem {
	i: string;
	/** Detail line. */
	d?: string;
	/** Duration, shown as a chip only when present. */
	t?: string;
}

export interface TimelineEvent {
	/** The date or span, shown above the text. Free text: "14 Mar – 2 Apr 2005" is legal. */
	when: string;
	i: string;
	src: string[];
}

export interface ChooseRule {
	/** The situation ("If you travel a lot"). */
	if: string;
	/** The pick for that situation. */
	then: string;
	src: string[];
}

export interface FactRow {
	k: string;
	v: string;
	src: string[];
}

export interface FlowNode {
	/** The id edges refer to ("n"). Unique within a block. */
	n: string;
	t: string;
	/** Detail shown when the node is tapped open. */
	d?: string;
	/** `kind:"decision"`: a node that branches on a question. */
	decision: boolean;
	src: string[];
}

export interface FlowEdge {
	from: string;
	to: string;
	/** The branch label ("Yes"), shown above the node it leads to. */
	l?: string;
}

export interface TabItem {
	tab: string;
	text: string;
}

/** One row of a `define` block: a term, its plain-English meaning, what else it is called, and an example. */
export interface DefineEntry {
	term: string;
	/** The plain-English meaning, in the model's own words, never a quote. */
	means: string;
	/** Other names for the same thing ("Also called"), at most 4. */
	also: string[];
	/** An example sentence, optional. */
	ex?: string;
}

/** One mapping row of an `analogy` block: the thing on the left ↔ the familiar one on the right. */
export interface AnalogyPair {
	x: string;
	y: string;
}

/**
 * One camp in a `views` block: who holds a position, a short stance headline,
 * the sentence saying it, and the sources for that line. `t` and `src` are
 * verified per line, like `claim`'s evidence lines; there is deliberately no
 * verdict and no strength meter (a model-invented weight of evidence is
 * unverifiable).
 */
export interface ViewCamp {
	who: string;
	stance: string;
	t: string;
	src: string[];
}

/**
 * A `readings` line's level, rendered in this fixed order however the model
 * emits them: start here, next, go deeper.
 */
export const READING_LEVELS = ['start', 'next', 'deep'] as const;
export type ReadingLevel = (typeof READING_LEVELS)[number];

/**
 * One page to read. `src` is the page's URL and must be a page web_read
 * actually fetched this turn — the renderer drops any line it can't match to
 * an opened page and counts it in a muted note. The minutes/page count shown
 * beside it are measured server-side (tools/page_stats.go), never written by
 * the model.
 */
export interface ReadingItem {
	lvl: ReadingLevel;
	t: string;
	why?: string;
	src: string[];
}

export const CLAIM_VERDICTS = ['true', 'mixed', 'misleading', 'false', 'unverified'] as const;
export type ClaimVerdict = (typeof CLAIM_VERDICTS)[number];

/** One Supports/Disputes line of a `claim` block. */
export interface ClaimEvidence {
	text: string;
	src: string[];
}

export type UiBlock =
	| { kind: 'callout'; tone: CalloutTone; text: string; asof?: string; src: string[] }
	| { kind: 'stat'; label?: string; value: string; note?: string; src: string[] }
	| { kind: 'compare'; cols: string[]; pick?: number; rows: CompareRow[] }
	| { kind: 'steps'; title?: string; steps: StepItem[] }
	| { kind: 'timeline'; events: TimelineEvent[] }
	| { kind: 'checklist'; title?: string; items: string[] }
	| { kind: 'procon'; proHead?: string; conHead?: string; pros: string[]; cons: string[] }
	| { kind: 'choose'; title?: string; rules: ChooseRule[] }
	| { kind: 'facts'; title?: string; sub?: string; rows: FactRow[] }
	// Nodes and edges are flat lists in arrival order: an edge can name a node
	// that has not streamed in yet, so layout (flowLayout.ts) is the component's
	// job, not the parser's.
	| { kind: 'flow'; nodes: FlowNode[]; edges: FlowEdge[] }
	| { kind: 'tabs'; tabs: TabItem[] }
	| { kind: 'disclose'; title?: string; hint?: string; paras: string[] }
	// Terms in plain English: the model's own definition, not a quote of a
	// source, so no source and no verification (accent block).
	| { kind: 'define'; entries: DefineEntry[] }
	// "X is like Y", with where the analogy stops being true. The model's own
	// teaching device: no source and no verification (accent block). `x`/`y` are
	// the two column headings; `breaks` is the point of the block.
	| { kind: 'analogy'; x?: string; y?: string; like: string; pairs: AnalogyPair[]; breaks?: string }
	// A contested question, by camp. No verdict and no strength meter on
	// purpose: only each camp's own line and sources are checked.
	| { kind: 'views'; title?: string; camps: ViewCamp[] }
	// Pages to read, in order: `lvl` fixes the display order and only pages
	// Polaris actually opened (a measured citation) get rendered.
	| { kind: 'readings'; title?: string; items: ReadingItem[] }
	| { kind: 'quote'; text: string; by?: string; src: string[] }
	// `verdict` is the model's own read, never a verified result: only the
	// evidence lines' sources get "found in source" ticks (decision 17).
	| { kind: 'claim'; text: string; verdict: ClaimVerdict; supports: ClaimEvidence[]; disputes: ClaimEvidence[] }
	// A line the grammar couldn't use: invalid JSON, unknown component, a child
	// line fitting no schema. Shown muted; never closes a container.
	| { kind: 'raw'; text: string };

export type UiBlockKind = UiBlock['kind'];
