import type { CalloutTone, CompareRow, StepItem, UiBlock } from './types';

// Caps from docs/plans/intelligent-ui.md "Grammar". Enforced here and nowhere
// else, so a component can trust what it is handed.
export const MAX_LINES_PER_FENCE = 40;
export const MAX_TEXT_CHARS = 400;
const MAX_COMPARE_ROWS = 12;
const MAX_STEPS = 15;
const MAX_RAW_CHARS = 200;

const TONES: readonly CalloutTone[] = ['note', 'warn', 'ok', 'answer'];

type Json = Record<string, unknown>;

// A text field: a string or a number, trimmed and clipped. Clipping (rather
// than the plan's "degrade the whole line to a raw row") keeps a long but
// otherwise good sentence readable instead of dumping its JSON on screen.
function text(v: unknown): string | undefined {
	if (typeof v === 'number' && Number.isFinite(v)) v = String(v);
	if (typeof v !== 'string') return undefined;
	const t = v.trim();
	if (!t) return undefined;
	return t.length > MAX_TEXT_CHARS ? t.slice(0, MAX_TEXT_CHARS - 1) + '…' : t;
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
			const cols = obj.cols.map(text).filter((c): c is string => c !== undefined);
			// 2-4 columns: one is not a comparison, five+ cannot fit a phone.
			if (cols.length < 2 || cols.length > 4 || cols.length !== obj.cols.length) return null;
			const pick = Number.isInteger(obj.pick) && (obj.pick as number) >= 0 && (obj.pick as number) < cols.length ? (obj.pick as number) : undefined;
			return { kind: 'compare', cols, pick, rows: [] };
		}
		case 'steps':
			return { kind: 'steps', title: text(obj.title), steps: [] };
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
	// callout / stat / raw take no children.
	return false;
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

		let obj: unknown;
		try {
			obj = JSON.parse(line);
		} catch {
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
