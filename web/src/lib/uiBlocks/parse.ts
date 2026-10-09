import { CLAIM_VERDICTS, type CalloutTone, type ClaimVerdict, type CompareRow, type StepItem, type UiBlock } from './types';

// Caps from docs/plans/intelligent-ui.md "Grammar". Enforced here and nowhere
// else, so a component can trust what it is handed.
export const MAX_LINES_PER_FENCE = 40;
export const MAX_TEXT_CHARS = 400;
// Up to six options fit: the wide table side-scrolls its data columns with the
// row-label column pinned, and the phone layout stacks one card per column, so
// width is no longer a reason to reject a comparison outright.
export const MAX_COMPARE_COLS = 6;
const MAX_COMPARE_ROWS = 12;
const MAX_STEPS = 15;
const MAX_TIMELINE_EVENTS = 15;
const MAX_CHECKLIST_ITEMS = 20;
const MAX_PROCON_PER_SIDE = 8;
const MAX_CHOOSE_RULES = 8;
const MAX_FACT_ROWS = 12;
const MAX_CLAIM_EVIDENCE_PER_SIDE = 6;
const MAX_FLOW_NODES = 10;
const MAX_FLOW_EDGES = 20;
const MAX_TABS = 6;
const MAX_DISCLOSE_PARAS = 8;
// Prose-body fields get a bigger clip than the 400-char default: a real model
// answering "how do I install X on each OS" puts a fenced command block in each
// tab (seen live), and a 400-char clip cut it off mid-command. These are hard
// safety clips, not the budget the prompt names — the base grammar asks for
// ~1500 (tab) and ~1200 (disclose) the same way it asks for ~300 against the
// 400 default, so the model stays under the clip rather than testing it.
export const MAX_TAB_TEXT_CHARS = 2000;
export const MAX_DISCLOSE_PARA_CHARS = 1200;
const MAX_RAW_CHARS = 200;

const TONES: readonly CalloutTone[] = ['note', 'warn', 'ok', 'answer'];

type Json = Record<string, unknown>;

// A text field: a string or a number, trimmed and clipped. Clipping (rather
// than the plan's "degrade the whole line to a raw row") keeps a long but
// otherwise good sentence readable instead of dumping its JSON on screen.
// Never pass this point-free to map/filter: their second argument is the index,
// which would silently become `max` (it did, once, and clipped every compare column).
function text(v: unknown, max = MAX_TEXT_CHARS): string | undefined {
	if (typeof v === 'number' && Number.isFinite(v)) v = String(v);
	if (typeof v !== 'string') return undefined;
	const t = v.trim();
	if (!t) return undefined;
	return t.length > max ? t.slice(0, max - 1) + '…' : t;
}

function sources(v: unknown): string[] {
	if (!Array.isArray(v)) return [];
	return v.filter((s): s is string => typeof s === 'string' && s.trim() !== '').slice(0, 6);
}

function raw(line: string): UiBlock {
	const t = line.trim();
	return { kind: 'raw', text: t.length > MAX_RAW_CHARS ? t.slice(0, MAX_RAW_CHARS - 1) + '…' : t };
}

/** A container line for the current block being built; null when invalid. */
function openContainer(obj: Json): UiBlock | null {
	switch (obj.c) {
		case 'callout': {
			const body = text(obj.text);
			if (!body) return null;
			const tone = TONES.includes(obj.tone as CalloutTone) ? (obj.tone as CalloutTone) : 'note';
			// "YYYY-MM", answer tone only: the as-of date is what makes a
			// bottom-line card honest about how fresh it is.
			const asof = tone === 'answer' && typeof obj.asof === 'string' && /^\d{4}-\d{2}$/.test(obj.asof) ? obj.asof : undefined;
			return { kind: 'callout', tone, text: body, asof, src: sources(obj.src) };
		}
		case 'stat': {
			const value = text(obj.value);
			if (!value) return null;
			return { kind: 'stat', label: text(obj.label), value, note: text(obj.note), src: sources(obj.src) };
		}
		case 'compare': {
			if (!Array.isArray(obj.cols)) return null;
			const cols = obj.cols.map((c) => text(c)).filter((c): c is string => c !== undefined);
			// One column is not a comparison; the renderer handles the rest.
			if (cols.length < 2 || cols.length > MAX_COMPARE_COLS || cols.length !== obj.cols.length) return null;
			const pick = Number.isInteger(obj.pick) && (obj.pick as number) >= 0 && (obj.pick as number) < cols.length ? (obj.pick as number) : undefined;
			return { kind: 'compare', cols, pick, rows: [] };
		}
		case 'steps':
			return { kind: 'steps', title: text(obj.title), steps: [] };
		// These open with no required field: their data is all in child lines,
		// so a bare `{"c":"timeline"}` is a valid (empty, for now) block.
		case 'timeline':
			return { kind: 'timeline', events: [] };
		case 'checklist':
			return { kind: 'checklist', title: text(obj.title), items: [] };
		case 'procon':
			return { kind: 'procon', proHead: text(obj.pro_h), conHead: text(obj.con_h), pros: [], cons: [] };
		case 'choose':
			return { kind: 'choose', title: text(obj.title), rules: [] };
		case 'facts':
			return { kind: 'facts', title: text(obj.title), sub: text(obj.sub), rows: [] };
		case 'flow':
			return { kind: 'flow', nodes: [], edges: [] };
		case 'tabs':
			return { kind: 'tabs', tabs: [] };
		case 'disclose':
			return { kind: 'disclose', title: text(obj.title), hint: text(obj.hint), paras: [] };
		case 'quote': {
			const body = text(obj.text);
			if (!body) return null;
			return { kind: 'quote', text: body, by: text(obj.by), src: sources(obj.src) };
		}
		case 'claim': {
			const body = text(obj.text);
			if (!body) return null;
			// An unknown verdict is "unverified", the neutral reading: never a
			// reason to drop a block that is otherwise fine.
			const verdict = CLAIM_VERDICTS.includes(obj.verdict as ClaimVerdict) ? (obj.verdict as ClaimVerdict) : 'unverified';
			return { kind: 'claim', text: body, verdict, supports: [], disputes: [] };
		}
		default:
			return null;
	}
}

/** Adds a child line to `block`. Returns false when it fits no schema or is over its cap. */
function addChild(block: UiBlock, obj: Json): boolean {
	if (block.kind === 'compare') {
		const label = text(obj.row);
		if (!label || !Array.isArray(obj.v) || block.rows.length >= MAX_COMPARE_ROWS) return false;
		// Wrong-length `v` is padded/truncated rather than rejected: a row
		// with a missing cell is still mostly right, and the grid stays aligned.
		const cells = obj.v as unknown[];
		const v = block.cols.map((_, i) => text(cells[i]) ?? '—');
		const row: CompareRow = { row: label, v, src: sources(obj.src) };
		block.rows.push(row);
		return true;
	}
	if (block.kind === 'steps') {
		const i = text(obj.i);
		if (!i || block.steps.length >= MAX_STEPS) return false;
		const step: StepItem = { i, d: text(obj.d), t: text(obj.t) };
		block.steps.push(step);
		return true;
	}
	if (block.kind === 'timeline') {
		const when = text(obj.when);
		const i = text(obj.i);
		if (!when || !i || block.events.length >= MAX_TIMELINE_EVENTS) return false;
		block.events.push({ when, i, src: sources(obj.src) });
		return true;
	}
	if (block.kind === 'checklist') {
		const i = text(obj.i);
		if (!i || block.items.length >= MAX_CHECKLIST_ITEMS) return false;
		block.items.push(i);
		return true;
	}
	if (block.kind === 'procon') {
		// "+" / "-" are the whole schema; a line carrying both is ambiguous, so
		// it fits neither and falls out as a raw row.
		const pro = text(obj['+']);
		const con = text(obj['-']);
		if (pro && !con && block.pros.length < MAX_PROCON_PER_SIDE) {
			block.pros.push(pro);
			return true;
		}
		if (con && !pro && block.cons.length < MAX_PROCON_PER_SIDE) {
			block.cons.push(con);
			return true;
		}
		return false;
	}
	if (block.kind === 'choose') {
		const cond = text(obj.if);
		const then = text(obj.then);
		if (!cond || !then || block.rules.length >= MAX_CHOOSE_RULES) return false;
		block.rules.push({ if: cond, then, src: sources(obj.src) });
		return true;
	}
	if (block.kind === 'facts') {
		const k = text(obj.k);
		const v = text(obj.v);
		if (!k || !v || block.rows.length >= MAX_FACT_ROWS) return false;
		block.rows.push({ k, v, src: sources(obj.src) });
		return true;
	}
	if (block.kind === 'flow') {
		if (Array.isArray(obj.e)) {
			// An edge: ["from","to"], optionally labelled. Ids are not checked
			// against the nodes here, because the target may stream in later.
			if (obj.e.length !== 2) return false;
			const from = text(obj.e[0]);
			const to = text(obj.e[1]);
			if (!from || !to || from === to || block.edges.length >= MAX_FLOW_EDGES) return false;
			block.edges.push({ from, to, l: text(obj.l) });
			return true;
		}
		const n = text(obj.n);
		const t = text(obj.t);
		if (!n || !t || block.nodes.length >= MAX_FLOW_NODES || block.nodes.some((x) => x.n === n)) return false;
		block.nodes.push({ n, t, d: text(obj.d), decision: obj.kind === 'decision', src: sources(obj.src) });
		return true;
	}
	if (block.kind === 'tabs') {
		const tab = text(obj.tab);
		const body = text(obj.text, MAX_TAB_TEXT_CHARS);
		if (!tab || !body || block.tabs.length >= MAX_TABS) return false;
		block.tabs.push({ tab, text: body });
		return true;
	}
	if (block.kind === 'disclose') {
		const p = text(obj.p, MAX_DISCLOSE_PARA_CHARS);
		if (!p || block.paras.length >= MAX_DISCLOSE_PARAS) return false;
		block.paras.push(p);
		return true;
	}
	if (block.kind === 'claim') {
		// Same "+" / "-" rule as procon: a line carrying both is ambiguous.
		const pro = text(obj['+']);
		const con = text(obj['-']);
		if (pro && !con && block.supports.length < MAX_CLAIM_EVIDENCE_PER_SIDE) {
			block.supports.push({ text: pro, src: sources(obj.src) });
			return true;
		}
		if (con && !pro && block.disputes.length < MAX_CLAIM_EVIDENCE_PER_SIDE) {
			block.disputes.push({ text: con, src: sources(obj.src) });
			return true;
		}
		return false;
	}
	// callout / stat / quote / raw take no children.
	return false;
}

/**
 * Parses one line, repairing a dropped closing bracket or brace; `undefined`
 * when the line is unusable. Mirrors parseLine in uiblocks/uiblocks.go.
 */
function parseLine(line: string): unknown {
	try {
		return JSON.parse(line);
	} catch {
		const fixed = closeBrackets(line);
		if (fixed === null) return undefined;
		try {
			return JSON.parse(fixed);
		} catch {
			return undefined;
		}
	}
}

/**
 * Returns `s` with any missing `]`/`}` inserted, or null when the structure is
 * too broken to guess at. Mirrors closeBrackets in uiblocks/uiblocks.go.
 *
 * A model occasionally drops a closing bracket (`"v":["a","b"}`), which used
 * to dump the whole row as visible JSON. Only brackets outside strings are
 * counted, a mismatched closer inserts the one it displaced (`]` before a `}`
 * that would close the enclosing object), an unterminated string or an
 * unmatched `]`/`}` gives up, and the caller re-parses whatever comes back — so
 * a wrong guess still falls through to a raw row.
 */
function closeBrackets(s: string): string | null {
	const out: string[] = [];
	const stack: string[] = [];
	let inString = false;
	let escaped = false;
	for (const ch of s) {
		if (inString) {
			out.push(ch);
			if (escaped) escaped = false;
			else if (ch === '\\') escaped = true;
			else if (ch === '"') inString = false;
			continue;
		}
		if (ch === '"') {
			inString = true;
			out.push(ch);
			continue;
		}
		if (ch === '[' || ch === '{') {
			stack.push(ch);
			out.push(ch);
			continue;
		}
		if (ch === ']') {
			if (stack[stack.length - 1] !== '[') return null;
			stack.pop();
			out.push(ch);
			continue;
		}
		if (ch === '}') {
			if (stack[stack.length - 1] === '{') {
				stack.pop();
				out.push(ch);
				continue;
			}
			// A `}` where a `]` was expected: the dropped `]` goes first.
			if (stack[stack.length - 1] !== '[') return null;
			stack.pop();
			out.push(']');
			if (stack[stack.length - 1] !== '{') return null;
			stack.pop();
			out.push('}');
			continue;
		}
		out.push(ch);
	}
	if (inString) return null;
	while (stack.length) out.push(stack.pop() === '[' ? ']' : '}');
	return out.join('');
}

/**
 * Parses a ```ui fence body into blocks. Total: never throws, for any input.
 *
 * Only lines already ended by a newline are parsed; a trailing partial line is
 * still arriving and is held back, which is what lets a block fill in row by
 * row while streaming without ever flashing a half-parsed row. (A cut-off turn
 * drops its partial line for the same reason.) Re-parsing the whole body on
 * every update is fine: the caps bound it to 40 lines.
 *
 * Guarantee the streaming design leans on: for any prefix of a body, the
 * number of blocks is never smaller than for a shorter prefix.
 */
export function parseUi(src: string): UiBlock[] {
	const lines = src.split('\n');
	lines.pop(); // the partial (or empty) tail after the last newline

	const blocks: UiBlock[] = [];
	let current: UiBlock | null = null;
	let used = 0;

	for (const line of lines) {
		if (!line.trim()) continue; // blank lines are ignored and don't count
		if (++used > MAX_LINES_PER_FENCE) {
			// One notice, not one raw row per excess line.
			blocks.push({ kind: 'raw', text: '… more lines than a block can hold' });
			break;
		}

		const obj = parseLine(line);
		if (obj === undefined) {
			blocks.push(raw(line));
			continue;
		}
		if (obj === null || typeof obj !== 'object' || Array.isArray(obj)) {
			blocks.push(raw(line));
			continue;
		}
		const o = obj as Json;

		if ('c' in o) {
			const opened = openContainer(o);
			if (opened) {
				blocks.push(opened);
				current = opened;
			} else {
				// Unknown component or invalid fields. Its child lines have no
				// container to join, so they fall out as raw rows too.
				blocks.push(raw(line));
				current = null;
			}
			continue;
		}

		if (!current || !addChild(current, o)) blocks.push(raw(line));
	}
	return blocks;
}
