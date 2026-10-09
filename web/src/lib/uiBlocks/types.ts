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
	| { kind: 'quote'; text: string; by?: string; src: string[] }
	// `verdict` is the model's own read, never a verified result: only the
	// evidence lines' sources get "found in source" ticks (decision 17).
	| { kind: 'claim'; text: string; verdict: ClaimVerdict; supports: ClaimEvidence[]; disputes: ClaimEvidence[] }
	// A line the grammar couldn't use: invalid JSON, unknown component, a child
	// line fitting no schema. Shown muted; never closes a container.
	| { kind: 'raw'; text: string };

export type UiBlockKind = UiBlock['kind'];
