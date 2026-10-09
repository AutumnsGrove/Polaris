import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { buildOracleNote, CHECK_DISPLAY, CHIP_NAMES, escapeHtml, focusSwitch, optionLabel } from './oracleLabels';
import type { OracleResult } from './types';

describe('escapeHtml', () => {
	it('neutralizes markup so a field name or unknown mode id cannot inject into {@html}', () => {
		expect(escapeHtml('<img src=x onerror="alert(1)">&')).toBe(
			'&lt;img src=x onerror=&quot;alert(1)&quot;&gt;&amp;'
		);
	});
});

describe('buildOracleNote', () => {
	it('escapes an unrecognized applied focus mode instead of passing it through', () => {
		const result: OracleResult = { checks: [] };
		const note = buildOracleNote(result, 'oracle', '<b>x</b>', undefined);
		expect(note).not.toContain('<b>x</b>');
		expect(note).toContain('&lt;b&gt;x&lt;/b&gt;');
	});

	it('reads high-stakes and the applied mode together', () => {
		const result: OracleResult = {
			checks: [{ key: 'high_stakes', winner: 'medical', probabilities: { medical: 0.9 }, fired: true }]
		};
		expect(buildOracleNote(result, 'oracle', 'researcher', undefined)).toBe(
			'Read as <b>medical</b> · answered as <b>Researcher</b>'
		);
	});

	describe('the ui (Prism) clause', () => {
		const ui = (winner: string, fired = true): OracleResult => ({
			checks: [{ key: 'ui', winner, probabilities: { [winner]: 0.9 }, fired }]
		});
		const answerWithBlock = 'Short answer.\n\n```ui\n{"c":"compare","cols":["A","B"]}\n```\n';

		it('names the block when the answer really contains one', () => {
			expect(buildOracleNote(ui('compare'), undefined, undefined, undefined, answerWithBlock)).toBe(
				'Shown as a <b>compare</b> block'
			);
		});

		it('claims nothing when the model was nudged but wrote no block', () => {
			expect(buildOracleNote(ui('compare'), undefined, undefined, undefined, 'Just prose.')).toBeNull();
			expect(buildOracleNote(ui('compare'), undefined, undefined, undefined, undefined)).toBeNull();
		});

		it('claims nothing for a check that did not fire, or an option this build cannot name', () => {
			expect(buildOracleNote(ui('compare', false), undefined, undefined, undefined, answerWithBlock)).toBeNull();
			expect(buildOracleNote(ui('map'), undefined, undefined, undefined, answerWithBlock)).toBeNull();
		});

		it('names the group (a) blocks it can draw', () => {
			expect(buildOracleNote(ui('timeline'), undefined, undefined, undefined, answerWithBlock)).toBe(
				'Shown as a <b>timeline</b> block'
			);
		});

		it('joins after the read-as and focus clauses, in that order', () => {
			const result: OracleResult = {
				checks: [
					{ key: 'task', winner: 'decide', probabilities: { decide: 0.9 }, fired: true },
					{ key: 'ui', winner: 'compare', probabilities: { compare: 0.9 }, fired: true }
				]
			};
			expect(buildOracleNote(result, 'oracle', 'shopper', undefined, answerWithBlock)).toBe(
				'Read as <b>a decision</b> · answered as <b>Shopper</b> · shown as a <b>compare</b> block'
			);
		});

		it('does not recognize an indented or tagged fence as a block', () => {
			expect(buildOracleNote(ui('steps'), undefined, undefined, undefined, '  ```ui\n{}\n```')).toBeNull();
			expect(buildOracleNote(ui('steps'), undefined, undefined, undefined, '```ui title\n{}\n```')).toBeNull();
		});

		it('matches exactly what the renderer splits: a longer fence counts, a quoted ```ui does not', () => {
			// Any 3+ backticks is a real `ui` fence to split.ts, so the note must still claim it.
			expect(buildOracleNote(ui('steps'), undefined, undefined, undefined, '````ui\n{"c":"steps"}\n````')).toBe(
				'Shown as a <b>steps</b> block'
			);
			// A ```ui line inside a longer fence is inert example code, not a block.
			expect(buildOracleNote(ui('steps'), undefined, undefined, undefined, '````md\n```ui\n{}\n```\n````')).toBeNull();
		});
	});

	it('says nothing when a check fired only on its no-op option', () => {
		const result: OracleResult = {
			checks: [{ key: 'high_stakes', winner: 'none', probabilities: { none: 0.95 }, fired: true }]
		};
		expect(buildOracleNote(result, undefined, undefined, undefined)).toBeNull();
	});

	it('reads a fired task when nothing more specific applies', () => {
		const result: OracleResult = {
			checks: [{ key: 'task', winner: 'troubleshoot', probabilities: { troubleshoot: 0.9 }, fired: true }]
		};
		expect(buildOracleNote(result, undefined, undefined, undefined)).toBe('Read as <b>a fix</b>');
	});

	it('prefers high-stakes over intent over task', () => {
		const result: OracleResult = {
			checks: [
				{ key: 'task', winner: 'plan', probabilities: { plan: 0.9 }, fired: true },
				{ key: 'intent', winner: 'travel', probabilities: { travel: 0.9 }, fired: true }
			]
		};
		expect(buildOracleNote(result, undefined, undefined, undefined)).toBe('Read as <b>travel</b>');
	});

	it('stays quiet for the default task option', () => {
		const result: OracleResult = {
			checks: [{ key: 'task', winner: 'answer', probabilities: { answer: 0.95 }, fired: true }]
		};
		expect(buildOracleNote(result, undefined, undefined, undefined)).toBeNull();
	});
});

// prompts.yaml's checks/chips are the source of truth for what Oracle asks;
// this file only decides how each is *displayed*. A check added to the YAML
// with no row here would render as a raw key in the turn-info sheet and get
// no star in the constellation, so fail loudly instead — the Go side has the
// same kind of drift test for config rules.
function promptsOracle(): { checks: Record<string, string[]>; chips: string[] } {
	const lines = readFileSync(resolve(process.cwd(), '..', 'prompts.yaml'), 'utf8').split('\n');
	const start = lines.findIndex((l) => l === 'oracle:');
	const checks: Record<string, string[]> = {};
	const chips: string[] = [];
	let section = '';
	let current = '';
	let inOptions = false;
	for (const line of lines.slice(start + 1)) {
		if (/^\S/.test(line)) break; // left the oracle: block
		if (line.trim() === '' || line.trim().startsWith('#')) continue;
		const indent = line.length - line.trimStart().length;
		if (indent === 2) {
			section = line.trim().replace(':', '');
			continue;
		}
		if (indent === 4 && (section === 'checks' || section === 'chips')) {
			current = line.trim().replace(':', '');
			if (section === 'checks') checks[current] = [];
			else chips.push(current);
			inOptions = false;
			continue;
		}
		if (section !== 'checks') continue;
		if (indent === 6) inOptions = line.trim() === 'options:';
		else if (indent === 8 && inOptions) checks[current].push(line.trim().split(':')[0]);
	}
	return { checks, chips };
}

describe('display config vs prompts.yaml', () => {
	const { checks, chips } = promptsOracle();

	it('parsed the checks it is guarding', () => {
		expect(Object.keys(checks)).toContain('focus');
		expect(chips).toContain('safari');
	});

	it('has a CHECK_DISPLAY row for every check', () => {
		const shown = CHECK_DISPLAY.map((d) => d.key);
		for (const key of Object.keys(checks)) expect(shown, `check ${key}`).toContain(key);
	});

	it('has a readable label for every option of every check', () => {
		for (const [key, options] of Object.entries(checks)) {
			for (const option of options) {
				// optionLabel falls back to the capitalized raw key, so a real label
				// is one that isn't just that fallback (snake_case would leak through).
				expect(optionLabel(key, option), `${key}.${option}`).not.toContain('_');
			}
		}
	});

	it('names every offer chip', () => {
		for (const chip of chips.filter((c) => c !== 'field')) expect(CHIP_NAMES[chip], `chip ${chip}`).toBeTruthy();
	});
});

describe('focusSwitch', () => {
	it('only reports a switch when Oracle picked a mode different from the previous turn', () => {
		expect(focusSwitch('oracle', 'shopper', 'brief')).toEqual({ from: 'brief', to: 'shopper' });
		expect(focusSwitch('oracle', 'shopper', 'shopper')).toBeNull();
		expect(focusSwitch('manual', 'shopper', 'brief')).toBeNull();
	});
});
