import { describe, it, expect } from 'vitest';
import { parseUi, MAX_LINES_PER_FENCE, MAX_TEXT_CHARS, MAX_TAB_TEXT_CHARS } from './parse';

// Golden fences: the property test below feeds every prefix of each to the
// parser, which is the guarantee streaming rendering leans on.
export const GOLDEN: Record<string, string> = {
	compare:
		'{"c":"compare","cols":["Moka pot","AeroPress"],"pick":1}\n' +
		'{"row":"Price","v":["$25","$40"],"src":["https://example.com/a"]}\n' +
		'{"row":"Cleanup","v":["Slow","Fast"]}\n',
	steps:
		'{"c":"steps","title":"Descale"}\n' +
		'{"i":"Mix vinegar and water","d":"Half and half","t":"2 min"}\n' +
		'{"i":"Run a cycle"}\n',
	timeline:
		'{"c":"timeline"}\n' +
		'{"when":"1969","i":"First message sent","src":["https://example.com/arpanet"]}\n' +
		'{"when":"14 Mar – 2 Apr 2005","i":"Long span"}\n',
	checklist: '{"c":"checklist","title":"Before you fly"}\n{"i":"Passport"}\n{"i":"Charger"}\n',
	procon:
		'{"c":"procon","pro_h":"Why","con_h":"Why not"}\n' +
		'{"+":"Fast"}\n{"-":"Costly"}\n{"+":"Quiet"}\n',
	choose:
		'{"c":"choose","title":"Which plan"}\n' +
		'{"if":"You travel a lot","then":"Annual","src":["https://example.com/p"]}\n' +
		'{"if":"You rarely do","then":"Pay as you go"}\n',
	facts:
		'{"c":"facts","title":"Lisbon","sub":"Capital of Portugal"}\n' +
		'{"k":"Population","v":"545,000","src":["https://example.com/pop"]}\n' +
		'{"k":"Founded","v":"c. 1200 BC"}\n',
	flow:
		'{"c":"flow"}\n' +
		'{"n":"a","t":"Check the light","d":"Is it blinking?","src":["https://example.com/led"]}\n' +
		'{"n":"b","t":"Replace the fuse","kind":"decision"}\n' +
		'{"e":["a","b"],"l":"Yes"}\n' +
		'{"e":["b","a"]}\n',
	tabs: '{"c":"tabs"}\n{"tab":"macOS","text":"Use `brew install`."}\n{"tab":"Linux","text":"Use apt."}\n',
	disclose: '{"c":"disclose","title":"Why this works","hint":"3 min read"}\n{"p":"First reason."}\n{"p":"Second reason."}\n',
	callout: '{"c":"callout","tone":"answer","text":"Yes, with caveats.","asof":"2026-10"}\n',
	stat: '{"c":"stat","label":"Boiling point","value":"100 °C","note":"at sea level"}\n',
	mixed:
		'{"c":"callout","text":"Heads up"}\n' +
		'not json at all\n' +
		'{"c":"nope"}\n' +
		'{"c":"stat","value":3}\n' +
		'{"orphan":true}\n'
};

describe('parseUi', () => {
	it('parses a compare block row by row', () => {
		const [b] = parseUi(GOLDEN.compare);
		expect(b).toEqual({
			kind: 'compare',
			cols: ['Moka pot', 'AeroPress'],
			pick: 1,
			rows: [
				{ row: 'Price', v: ['$25', '$40'], src: ['https://example.com/a'] },
				{ row: 'Cleanup', v: ['Slow', 'Fast'], src: [] }
			]
		});
	});

	it('pads and truncates a wrong-length v, and ignores an out-of-range pick', () => {
		const [b] = parseUi('{"c":"compare","cols":["A","B"],"pick":5}\n{"row":"x","v":["1"]}\n{"row":"y","v":["1","2","3"]}\n');
		expect(b).toMatchObject({ kind: 'compare', pick: undefined });
		expect((b as { rows: { v: string[] }[] }).rows.map((r) => r.v)).toEqual([['1', '—'], ['1', '2']]);
	});

	it('rejects a compare with fewer than 2 or more than 4 columns', () => {
		expect(parseUi('{"c":"compare","cols":["A"]}\n')[0].kind).toBe('raw');
		expect(parseUi('{"c":"compare","cols":["A","B","C","D","E"]}\n')[0].kind).toBe('raw');
	});

	it('parses steps with optional detail and duration', () => {
		const [b] = parseUi(GOLDEN.steps);
		expect(b).toEqual({
			kind: 'steps',
			title: 'Descale',
			steps: [
				{ i: 'Mix vinegar and water', d: 'Half and half', t: '2 min' },
				{ i: 'Run a cycle', d: undefined, t: undefined }
			]
		});
	});

	it('parses the group (a) blocks, including ones whose container line carries no data', () => {
		expect(parseUi(GOLDEN.timeline)[0]).toEqual({
			kind: 'timeline',
			events: [
				{ when: '1969', i: 'First message sent', src: ['https://example.com/arpanet'] },
				{ when: '14 Mar – 2 Apr 2005', i: 'Long span', src: [] }
			]
		});
		expect(parseUi(GOLDEN.checklist)[0]).toEqual({ kind: 'checklist', title: 'Before you fly', items: ['Passport', 'Charger'] });
		expect(parseUi(GOLDEN.procon)[0]).toEqual({
			kind: 'procon',
			proHead: 'Why',
			conHead: 'Why not',
			pros: ['Fast', 'Quiet'],
			cons: ['Costly']
		});
		expect(parseUi(GOLDEN.choose)[0]).toMatchObject({
			kind: 'choose',
			rules: [
				{ if: 'You travel a lot', then: 'Annual', src: ['https://example.com/p'] },
				{ if: 'You rarely do', then: 'Pay as you go', src: [] }
			]
		});
		expect(parseUi(GOLDEN.facts)[0]).toMatchObject({
			kind: 'facts',
			title: 'Lisbon',
			sub: 'Capital of Portugal',
			rows: [
				{ k: 'Population', v: '545,000', src: ['https://example.com/pop'] },
				{ k: 'Founded', v: 'c. 1200 BC', src: [] }
			]
		});
	});

	it('parses flow nodes and edges as flat lists, keeping an edge whose target has not arrived', () => {
		expect(parseUi(GOLDEN.flow)[0]).toEqual({
			kind: 'flow',
			nodes: [
				{ n: 'a', t: 'Check the light', d: 'Is it blinking?', decision: false, src: ['https://example.com/led'] },
				{ n: 'b', t: 'Replace the fuse', d: undefined, decision: true, src: [] }
			],
			edges: [
				{ from: 'a', to: 'b', l: 'Yes' },
				{ from: 'b', to: 'a', l: undefined }
			]
		});
		// An edge to a node that arrives later is legal: layout, not the parser, resolves it.
		expect(parseUi('{"c":"flow"}\n{"n":"a","t":"A"}\n{"e":["a","later"]}\n')[0]).toMatchObject({
			edges: [{ from: 'a', to: 'later' }]
		});
	});

	it('rejects flow lines that cannot be used and enforces the node cap', () => {
		const kinds = (s: string) => parseUi(s).map((b) => b.kind);
		// duplicate id, self-loop, malformed edge, node with no title
		expect(kinds('{"c":"flow"}\n{"n":"a","t":"A"}\n{"n":"a","t":"Again"}\n')).toEqual(['flow', 'raw']);
		expect(kinds('{"c":"flow"}\n{"e":["a","a"]}\n')).toEqual(['flow', 'raw']);
		expect(kinds('{"c":"flow"}\n{"e":["a"]}\n')).toEqual(['flow', 'raw']);
		expect(kinds('{"c":"flow"}\n{"n":"a"}\n')).toEqual(['flow', 'raw']);
		const many = '{"c":"flow"}\n' + Array.from({ length: 12 }, (_, i) => `{"n":"n${i}","t":"N${i}"}\n`).join('');
		const [flow, ...rest] = parseUi(many);
		expect((flow as { nodes: unknown[] }).nodes).toHaveLength(10);
		expect(rest.map((b) => b.kind)).toEqual(['raw', 'raw']);
	});

	it('parses tabs and disclose', () => {
		expect(parseUi(GOLDEN.tabs)[0]).toEqual({
			kind: 'tabs',
			tabs: [
				{ tab: 'macOS', text: 'Use `brew install`.' },
				{ tab: 'Linux', text: 'Use apt.' }
			]
		});
		expect(parseUi(GOLDEN.disclose)[0]).toEqual({
			kind: 'disclose',
			title: 'Why this works',
			hint: '3 min read',
			paras: ['First reason.', 'Second reason.']
		});
		const seven = '{"c":"tabs"}\n' + Array.from({ length: 7 }, (_, i) => `{"tab":"T${i}","text":"x"}\n`).join('');
		expect(parseUi(seven).map((b) => b.kind)).toEqual(['tabs', 'raw']);
	});

	it('lets a tab or disclosed paragraph run past the 400-char default, up to its own cap', () => {
		const long = 'x'.repeat(MAX_TEXT_CHARS + 50);
		const tab = parseUi(`{"c":"tabs"}\n${JSON.stringify({ tab: 'A', text: long })}\n`)[0] as { tabs: { text: string }[] };
		expect(tab.tabs[0].text).toBe(long);
		const over = parseUi(`{"c":"tabs"}\n${JSON.stringify({ tab: 'A', text: 'x'.repeat(MAX_TAB_TEXT_CHARS + 10) })}\n`)[0] as { tabs: { text: string }[] };
		expect(over.tabs[0].text).toHaveLength(MAX_TAB_TEXT_CHARS);
		expect(over.tabs[0].text.endsWith('…')).toBe(true);
		const para = parseUi(`{"c":"disclose"}\n${JSON.stringify({ p: long })}\n`)[0] as { paras: string[] };
		expect(para.paras[0]).toBe(long);
		// every other field keeps the 400-char clip
		const step = parseUi(`{"c":"steps"}\n${JSON.stringify({ i: long })}\n`)[0] as { steps: { i: string }[] };
		expect(step.steps[0].i).toHaveLength(MAX_TEXT_CHARS);
	});

	it('sends a child line that fits its group (a) container to a raw row', () => {
		const kinds = (s: string) => parseUi(s).map((b) => b.kind);
		expect(kinds('{"c":"timeline"}\n{"i":"no date"}\n')).toEqual(['timeline', 'raw']);
		expect(kinds('{"c":"procon"}\n{"+":"a","-":"b"}\n')).toEqual(['procon', 'raw']);
		expect(kinds('{"c":"choose"}\n{"if":"x"}\n')).toEqual(['choose', 'raw']);
		expect(kinds('{"c":"facts"}\n{"k":"only a key"}\n')).toEqual(['facts', 'raw']);
		expect(kinds('{"c":"checklist"}\n{"row":"wrong schema"}\n')).toEqual(['checklist', 'raw']);
	});

	it('only keeps asof on an answer callout, in YYYY-MM form', () => {
		expect(parseUi(GOLDEN.callout)[0]).toMatchObject({ tone: 'answer', asof: '2026-10' });
		expect(parseUi('{"c":"callout","tone":"warn","text":"x","asof":"2026-10"}\n')[0]).toMatchObject({ asof: undefined });
		expect(parseUi('{"c":"callout","tone":"answer","text":"x","asof":"last week"}\n')[0]).toMatchObject({ asof: undefined });
	});

	it('defaults an unknown tone to note', () => {
		expect(parseUi('{"c":"callout","tone":"loud","text":"x"}\n')[0]).toMatchObject({ tone: 'note' });
	});

	it('coerces a numeric value to text', () => {
		expect(parseUi('{"c":"stat","value":3}\n')[0]).toMatchObject({ kind: 'stat', value: '3' });
	});

	it('turns bad lines into raw rows without disturbing neighbours', () => {
		const out = parseUi(GOLDEN.mixed);
		expect(out.map((b) => b.kind)).toEqual(['callout', 'raw', 'raw', 'stat', 'raw']);
		expect(out[1]).toEqual({ kind: 'raw', text: 'not json at all' });
	});

	it('makes child lines before any container, or after an invalid one, raw rows', () => {
		expect(parseUi('{"row":"x","v":["1","2"]}\n')[0].kind).toBe('raw');
		expect(parseUi('{"c":"nope"}\n{"i":"step"}\n').map((b) => b.kind)).toEqual(['raw', 'raw']);
	});

	it('does not let a non-fitting child close its container', () => {
		const out = parseUi('{"c":"steps"}\n{"i":"one"}\n{"junk":1}\n{"i":"two"}\n');
		expect(out.map((b) => b.kind)).toEqual(['steps', 'raw']);
		expect((out[0] as { steps: unknown[] }).steps).toHaveLength(2);
	});

	it('holds back a trailing partial line', () => {
		expect(parseUi('{"c":"stat","value":"1"}\n{"c":"stat","val')).toHaveLength(1);
		// Complete JSON but no newline yet: still held back.
		expect(parseUi('{"c":"stat","value":"1"}')).toHaveLength(0);
	});

	it('ignores blank lines', () => {
		expect(parseUi('\n\n{"c":"stat","value":"1"}\n\n')).toHaveLength(1);
	});

	it('clips an over-long text field', () => {
		const long = 'x'.repeat(MAX_TEXT_CHARS + 50);
		const [b] = parseUi(`{"c":"callout","text":"${long}"}\n`);
		expect((b as { text: string }).text).toHaveLength(MAX_TEXT_CHARS);
	});

	it('caps child rows (compare 12, steps 15) with the overflow as raw rows', () => {
		const rows = Array.from({ length: 14 }, (_, i) => `{"row":"r${i}","v":["a","b"]}\n`).join('');
		const out = parseUi('{"c":"compare","cols":["A","B"]}\n' + rows);
		expect((out[0] as { rows: unknown[] }).rows).toHaveLength(12);
		expect(out.filter((b) => b.kind === 'raw')).toHaveLength(2);
	});

	it('stops after the per-fence line cap with a single notice', () => {
		const lines = Array.from({ length: MAX_LINES_PER_FENCE + 10 }, () => '{"c":"stat","value":"1"}\n').join('');
		const out = parseUi(lines);
		expect(out).toHaveLength(MAX_LINES_PER_FENCE + 1);
		expect(out.at(-1)?.kind).toBe('raw');
	});

	it('never throws on hostile input', () => {
		for (const s of ['\u0000\n', '[]\n', 'null\n', '"str"\n', '{"c":null}\n', '{"c":"compare","cols":"AB"}\n', '{"c":"compare","cols":[1,2]}\n{"row":1,"v":"x"}\n', '{"__proto__":{"c":"stat"}}\n', '{"c":"stat","value":{"a":1}}\n']) {
			expect(() => parseUi(s)).not.toThrow();
		}
	});
});

describe('parseUi streaming guarantee', () => {
	for (const [name, body] of Object.entries(GOLDEN)) {
		it(`never throws and never loses a block, at every byte offset of "${name}"`, () => {
			let prev = 0;
			for (let n = 0; n <= body.length; n++) {
				let blocks;
				expect(() => (blocks = parseUi(body.slice(0, n)))).not.toThrow();
				const count = (blocks as unknown as unknown[]).length;
				expect(count).toBeGreaterThanOrEqual(prev);
				prev = count;
			}
		});
	}
});
