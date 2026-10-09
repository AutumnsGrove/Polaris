import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { flattenAnswer } from './flatten';

// The same cases uiblocks runs (uiblocks_test.go). They are the
// contract between the two implementations: a drift in either one fails here
// or there, instead of the UI and the server quietly disagreeing.
// Resolved from the working directory (vitest runs from web/): import.meta.url
// is not a file: URL once vite has transformed this module.
const fixture = JSON.parse(readFileSync(resolve(process.cwd(), '../testdata/ui_flatten.json'), 'utf8')) as { cases: { name: string; input: string; want: string }[] };

describe('flattenAnswer (shared fixture)', () => {
	it('loaded cases', () => expect(fixture.cases.length).toBeGreaterThan(0));
	for (const c of fixture.cases) {
		it(c.name, () => expect(flattenAnswer(c.input)).toBe(c.want));
	}
});

describe('flattenAnswer', () => {
	it('never throws on any prefix of a mixed answer', () => {
		const src =
			'intro\n\n```ui\n{"c":"compare","cols":["A","B"],"pick":0}\n{"row":"x","v":["1","2"]}\n{"c":"steps"}\n{"i":"s"}\n```\n\nend\n```mermaid\ngraph TD\n```\n';
		for (let n = 0; n <= src.length; n++) expect(() => flattenAnswer(src.slice(0, n))).not.toThrow();
	});

	it('is idempotent', () => {
		const once = flattenAnswer('a\n```ui\n{"c":"stat","value":"1"}\n```\nb\n');
		expect(flattenAnswer(once)).toBe(once);
	});
});
