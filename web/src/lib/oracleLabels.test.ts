import { describe, it, expect } from 'vitest';
import { buildOracleNote, escapeHtml, focusSwitch } from './oracleLabels';
import type { OracleResult } from './types';

describe('escapeHtml', () => {
	it('neutralizes markup so a project name or unknown mode id cannot inject into {@html}', () => {
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

	it('says nothing when a check fired only on its no-op option', () => {
		const result: OracleResult = {
			checks: [{ key: 'high_stakes', winner: 'none', probabilities: { none: 0.95 }, fired: true }]
		};
		expect(buildOracleNote(result, undefined, undefined, undefined)).toBeNull();
	});
});

describe('focusSwitch', () => {
	it('only reports a switch when Oracle picked a mode different from the previous turn', () => {
		expect(focusSwitch('oracle', 'shopper', 'brief')).toEqual({ from: 'brief', to: 'shopper' });
		expect(focusSwitch('oracle', 'shopper', 'shopper')).toBeNull();
		expect(focusSwitch('manual', 'shopper', 'brief')).toBeNull();
	});
});
