import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// Same constraint as mermaid.test.ts: happy-dom can't run the real mermaid, so
// it is mocked. parse() accepts a source only if every complete line is one
// the fake grammar knows ("graph ..." or an "X-->Y" edge); render() embeds
// the source so a test can see exactly which prefix is on screen.
const initialize = vi.fn();
const parse = vi.fn(async (source: string) => {
	const lines = source.split('\n').filter((l) => l.trim());
	const ok = lines.length > 0 && lines.every((l) => /^graph\b/.test(l) || /^\w+-->\w+$/.test(l));
	return ok ? { diagramType: 'flowchart' } : false;
});
const render = vi.fn(async (id: string, source: string) => ({ svg: `<svg data-id="${id}">${source}</svg>` }));

vi.mock('mermaid', () => ({ default: { initialize, parse, render } }));
vi.mock('./clipboard', () => ({ copyToClipboard: vi.fn(async () => {}) }));

const { mountMermaidStream } = await import('./mermaid');

// Lets the stream's async pump (dynamic import, parse, render) settle.
async function settle() {
	for (let i = 0; i < 20; i++) await Promise.resolve();
	await new Promise((r) => setTimeout(r, 0));
}

const shown = (host: HTMLElement) => host.querySelector('.mermaid-render')?.innerHTML ?? '';

describe('mountMermaidStream', () => {
	let host: HTMLElement;
	beforeEach(() => {
		host = document.createElement('div');
		document.body.appendChild(host);
		parse.mockClear();
		render.mockClear();
		// Zero the throttle: these tests exercise ordering, not pacing.
		vi.spyOn(performance, 'now').mockReturnValue(0);
	});
	afterEach(() => {
		vi.restoreAllMocks();
		host.remove();
	});

	it('shows a placeholder until the first valid prefix renders', async () => {
		const s = mountMermaidStream(host);
		s.update('graph TD\nA-->', false);
		await settle();
		// Only "graph TD\n" is a complete line, and it is valid by itself.
		expect(host.querySelector('.mermaid-pending')).toBeNull();
		expect(shown(host)).toContain('graph TD');
		s.destroy();
	});

	it('renders only complete lines, never the partial one', async () => {
		const s = mountMermaidStream(host);
		s.update('graph TD\nA-->B\nB-->', false);
		await settle();
		expect(shown(host)).toContain('A--&gt;B');
		expect(shown(host)).not.toContain('B--&gt;\n');
		expect(render).toHaveBeenLastCalledWith(expect.any(String), 'graph TD\nA-->B\n');
		s.destroy();
	});

	it('keeps the last good render when a later prefix does not parse', async () => {
		const s = mountMermaidStream(host);
		s.update('graph TD\nA-->B\n', false);
		await settle();
		const good = shown(host);
		expect(good).toContain('A--&gt;B');

		s.update('graph TD\nA-->B\nthis is not valid\n', false);
		await settle();
		expect(shown(host)).toBe(good);
		expect(host.querySelector('.mermaid-error-note')).toBeNull();
		s.destroy();
	});

	it('does not re-render when no new complete line has arrived', async () => {
		const s = mountMermaidStream(host);
		s.update('graph TD\nA-->B\n', false);
		await settle();
		const calls = render.mock.calls.length;
		s.update('graph TD\nA-->B\nC', false);
		await settle();
		expect(render.mock.calls.length).toBe(calls);
		s.destroy();
	});

	it('updates one wrapper in place instead of rebuilding it', async () => {
		const s = mountMermaidStream(host);
		s.update('graph TD\nA-->B\n', false);
		await settle();
		const wrapper = host.querySelector('.mermaid-diagram');
		s.update('graph TD\nA-->B\nB-->C\n', false);
		await settle();
		expect(host.querySelector('.mermaid-diagram')).toBe(wrapper);
		expect(shown(host)).toContain('B--&gt;C');
		expect(wrapper?.getAttribute('data-mermaid-source')).toContain('B-->C');
		s.destroy();
	});

	it('does a final render on the closing fence and shows the source on failure', async () => {
		const s = mountMermaidStream(host);
		s.update('graph TD\nA-->B\n', false);
		await settle();
		// Final text is invalid as a whole (the mock render() accepts anything,
		// so make the final render itself fail). One rejection is enough: the
		// auto-quote retry is skipped when it has nothing to repair, so a
		// second queued rejection would leak into the next test.
		render.mockRejectedValueOnce(new Error('bad'));
		s.update('graph TD\nA-->B\nnonsense\n', true);
		await settle();
		const pre = host.querySelector('pre[data-mermaid-failed]');
		expect(pre).not.toBeNull();
		expect(pre?.textContent).toContain('nonsense');
		expect(host.querySelector('.mermaid-error-note')).not.toBeNull();
		s.destroy();
	});

	it('renders a closed fence straight away, with no placeholder', async () => {
		const s = mountMermaidStream(host);
		s.update('graph TD\nA-->B\n', true);
		await settle();
		expect(host.querySelector('.mermaid-pending')).toBeNull();
		expect(shown(host)).toContain('A--&gt;B');
		s.destroy();
	});

	it('stops touching the DOM after destroy', async () => {
		const s = mountMermaidStream(host);
		s.destroy();
		s.update('graph TD\nA-->B\n', true);
		await settle();
		expect(host.childElementCount).toBe(0);
	});
});
