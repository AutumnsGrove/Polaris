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
	// A line the grammar couldn't use: invalid JSON, unknown component, a child
	// line fitting no schema. Shown muted; never closes a container.
	| { kind: 'raw'; text: string };

export type UiBlockKind = UiBlock['kind'];
