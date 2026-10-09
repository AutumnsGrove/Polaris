import { parseUi } from './parse';
import { splitContent } from './split';
import type { UiBlock } from './types';

// The plain-text form of an answer's `ui` blocks, for every consumer that is
// NOT the chat renderer: copy, read-aloud, and anything else that would
// otherwise show or speak raw JSON lines. Go twin: gateway/uiblocks
// (Flatten). Both are tested against testdata/ui_flatten.json, so they cannot
// drift apart unnoticed.

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

// "2026-10" -> "Oct 2026" from a fixed table, not Intl: locale formatting
// would make this differ from the Go side on some machines.
function asofLabel(a: string): string {
	const m = Number(a.slice(5, 7));
	return m >= 1 && m <= 12 ? `${MONTHS[m - 1]} ${a.slice(0, 4)}` : a;
}

function host(u: string): string {
	try {
		return new URL(u).hostname || u;
	} catch {
		return u;
	}
}

// Links in the schema's canonical order (container fields, then child lines in
// arrival order) — never key order — because verification's claim_index
// matching depends on client and server agreeing on it.
function links(src: string[]): string {
	return src.map((u) => `[${host(u)}](${u})`).join(' ');
}

function withSources(s: string, src: string[]): string {
	return src.length ? `${s} ${links(src)}` : s;
}

function flattenBlocks(blocks: UiBlock[]): string[] {
	const out: string[] = [];
	for (const b of blocks) {
		switch (b.kind) {
			case 'callout': {
				const s = b.asof ? `${b.text} (as of ${asofLabel(b.asof)})` : b.text;
				out.push(withSources(s, b.src));
				break;
			}
			case 'stat': {
				let s = b.label ? `${b.label}: ${b.value}` : b.value;
				if (b.note) s += ` (${b.note})`;
				out.push(withSources(s, b.src));
				break;
			}
			case 'compare': {
				b.cols.forEach((col, ci) => {
					let line = `${col}: ${b.rows.map((r) => `${r.row} ${r.v[ci]}`).join('; ')}`;
					if (b.pick === ci) line += ' (recommended)';
					out.push(line);
				});
				const src = b.rows.flatMap((r) => r.src);
				if (src.length) out.push(links(src));
				break;
			}
			case 'steps':
				if (b.title) out.push(`${b.title}:`);
				b.steps.forEach((s, n) => {
					let line = `${n + 1}. ${s.i}`;
					if (s.d) line += ` — ${s.d}`;
					if (s.t) line += ` (${s.t})`;
					out.push(line);
				});
				break;
			case 'timeline':
				for (const ev of b.events) out.push(withSources(`${ev.when}: ${ev.i}`, ev.src));
				break;
			case 'checklist':
				if (b.title) out.push(`${b.title}:`);
				for (const item of b.items) out.push(`- ${item}`);
				break;
			case 'procon':
				if (b.pros.length) out.push(`${b.proHead ?? 'Pros'}: ${b.pros.join('; ')}`);
				if (b.cons.length) out.push(`${b.conHead ?? 'Cons'}: ${b.cons.join('; ')}`);
				break;
			case 'choose':
				if (b.title) out.push(`${b.title}:`);
				for (const r of b.rules) out.push(withSources(`If ${r.if}: ${r.then}`, r.src));
				break;
			case 'facts': {
				const head = b.title && b.sub ? `${b.title} — ${b.sub}` : (b.title ?? b.sub);
				if (head) out.push(`${head}:`);
				for (const r of b.rows) out.push(withSources(`${r.k}: ${r.v}`, r.src));
				break;
			}
			case 'flow': {
				// Nodes then edges, both in arrival order; the layout (BFS, back-edges) is
				// a display concern and is not repeated on the Go side. An edge whose
				// ends never arrived is dropped.
				const title = new Map(b.nodes.map((n) => [n.n, n.t]));
				for (const n of b.nodes) out.push(withSources(n.d ? `${n.t} — ${n.d}` : n.t, n.src));
				for (const e of b.edges) {
					const from = title.get(e.from);
					const to = title.get(e.to);
					if (from !== undefined && to !== undefined) out.push(`${from} → ${to}${e.l ? ` (${e.l})` : ''}`);
				}
				break;
			}
			case 'tabs':
				for (const t of b.tabs) out.push(`${t.tab}: ${t.text}`);
				break;
			case 'disclose':
				if (b.title) out.push(`${b.title}:`);
				for (const p of b.paras) out.push(p);
				break;
			// 'raw' rows are what the grammar could not use: noise to any reader.
		}
	}
	return out;
}

/**
 * Returns `content` with every column-0 ```ui fence replaced by readable text
 * and everything else untouched. Content with no ui fence comes back as-is.
 */
export function flattenAnswer(content: string): string {
	if (!content.includes('ui')) return content;
	return splitContent(content, false, ['ui'])
		.map((seg) => {
			if (seg.kind === 'md') return seg.text;
			const lines = flattenBlocks(parseUi(seg.src));
			return lines.length ? lines.join('\n') + '\n' : '';
		})
		.join('');
}
