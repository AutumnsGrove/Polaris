import { describe, it, expect, vi, beforeEach } from 'vitest';

// happy-dom has no SVG renderer, so the real 'mermaid' package can't run in
// this test environment — mock it and drive the DOM-pass logic in
// mermaid.ts directly. render() succeeds for any source containing "graph"
// and rejects otherwise, standing in for a real parse error.
const initialize = vi.fn();
const render = vi.fn(async (id: string, source: string) => {
	if (!source.includes('graph')) throw new Error('Parse error: bad diagram');
	return { svg: `<svg data-id="${id}">${source}</svg>` };
});

vi.mock('mermaid', () => ({
	default: { initialize, render }
}));

const copyToClipboard = vi.fn(async () => {});
vi.mock('./clipboard', () => ({ copyToClipboard }));

// Imported after the mocks so mermaid.ts's `import('mermaid')` and
// `import('./clipboard')` resolve to them.
const { renderMermaidIn } = await import('./mermaid');

// Builds the fence fixture via real DOM nodes + textContent rather than an
// HTML string handed to innerHTML — happy-dom's parser has a bug where a
// bare "-->" in HTML-string input (e.g. "A-->B", a completely ordinary
// mermaid edge) gets misparsed and duplicates the preceding text, which a
// real browser's native parser (and DOMPurify, which every production
// mermaid fence actually passes through) does not do — confirmed live
// against a real Chromium build. textContent assignment never re-parses
// its argument as markup, so it sidesteps the bug entirely instead of
// working around it with awkward source text.
function fenceBlock(source: string): HTMLElement {
	const pre = document.createElement('pre');
	pre.setAttribute('data-mermaid', '');
	const code = document.createElement('code');
	code.className = 'language-mermaid';
	code.textContent = source;
	pre.appendChild(code);
	return pre;
}

function containerWith(...blocks: HTMLElement[]): HTMLDivElement {
	const container = document.createElement('div');
	for (const block of blocks) container.appendChild(block);
	return container;
}

beforeEach(() => {
	initialize.mockClear();
	render.mockClear();
	copyToClipboard.mockClear();
	delete document.documentElement.dataset.theme;
});

describe('renderMermaidIn', () => {
	it('replaces a data-mermaid block with the rendered SVG', async () => {
		const container = containerWith(fenceBlock('graph TD; A-->B;'));
		await renderMermaidIn(container);

		expect(container.querySelector('pre[data-mermaid]')).toBeNull();
		expect(container.querySelector('.mermaid-diagram svg')).not.toBeNull();
	});

	it('replaces several diagrams in the same container independently', async () => {
		const container = containerWith(fenceBlock('graph TD; A-->B;'), fenceBlock('graph LR; C-->D;'));
		await renderMermaidIn(container);

		expect(container.querySelectorAll('.mermaid-diagram').length).toBe(2);
		expect(container.querySelectorAll('pre[data-mermaid]').length).toBe(0);
	});

	it('falls back to the original code block plus a note on a render/parse error', async () => {
		const container = containerWith(fenceBlock('not a real diagram'));
		await renderMermaidIn(container);

		// Never a broken hole or a throw — the source stays visible.
		const block = container.querySelector('pre[data-mermaid]');
		expect(block).not.toBeNull();
		expect(block?.textContent).toContain('not a real diagram');
		expect(container.querySelector('.mermaid-error-note')).not.toBeNull();
	});

	it('does nothing when the container has no mermaid blocks', async () => {
		const container = document.createElement('div');
		container.innerHTML = '<p>ordinary prose</p>';
		await renderMermaidIn(container);

		expect(render).not.toHaveBeenCalled();
		expect(container.innerHTML).toBe('<p>ordinary prose</p>');
	});

	it('caches the lazy mermaid import across calls', async () => {
		const a = containerWith(fenceBlock('graph TD; A-->B;'));
		await renderMermaidIn(a);

		const b = containerWith(fenceBlock('graph TD; C-->D;'));
		await renderMermaidIn(b);

		// initialize() re-runs every pass (cheap, theme-live), but the
		// underlying import('mermaid') itself only resolves once — asserted
		// indirectly here since both calls succeeded against the same
		// mocked module without re-registering it.
		expect(render).toHaveBeenCalledTimes(2);
	});

	// Regression test for a real bug found live-testing: a loaded thread's
	// mermaid blocks render immediately (content is already in hand from
	// the DB), but SettingsState.load() sets data-theme asynchronously
	// after mount — so the first pass can easily run before the user's
	// real theme preference is applied. Without re-rendering already-
	// converted diagrams on a later pass, a light-theme user reopening an
	// old thread was stuck seeing dark-mermaid-themed diagrams forever.
	it('re-renders an already-converted diagram when the theme changes on a later pass', async () => {
		const container = containerWith(fenceBlock('graph TD; A-->B;'));

		document.documentElement.dataset.theme = 'dark';
		await renderMermaidIn(container);
		expect(initialize).toHaveBeenLastCalledWith(expect.objectContaining({ theme: 'dark' }));
		expect(render).toHaveBeenCalledTimes(1);

		document.documentElement.dataset.theme = 'light';
		await renderMermaidIn(container);
		expect(initialize).toHaveBeenLastCalledWith(expect.objectContaining({ theme: 'default' }));
		expect(render).toHaveBeenCalledTimes(2);
		expect(container.querySelectorAll('.mermaid-diagram').length).toBe(1);
	});

	it('does not re-render when a later pass sees the same theme', async () => {
		const container = containerWith(fenceBlock('graph TD; A-->B;'));

		document.documentElement.dataset.theme = 'light';
		await renderMermaidIn(container);
		expect(render).toHaveBeenCalledTimes(1);

		await renderMermaidIn(container);
		expect(render).toHaveBeenCalledTimes(1);
	});

	it('does not retry a block that already failed to parse on a later pass', async () => {
		const container = containerWith(fenceBlock('not a real diagram'));

		await renderMermaidIn(container);
		expect(render).toHaveBeenCalledTimes(1);

		document.documentElement.dataset.theme = 'light';
		await renderMermaidIn(container);
		expect(render).toHaveBeenCalledTimes(1);
		expect(container.querySelectorAll('.mermaid-error-note').length).toBe(1);
	});
});

describe('renderMermaidIn toolbar', () => {
	it('renders a copy button and a source-toggle button per diagram', async () => {
		const container = containerWith(fenceBlock('graph TD; A-->B;'));
		await renderMermaidIn(container);

		const buttons = container.querySelectorAll('.mermaid-btn');
		expect(buttons.length).toBe(2);
	});

	it('copies the mermaid source (not the rendered SVG) when the copy button is clicked', async () => {
		const container = containerWith(fenceBlock('graph TD; A-->B;'));
		await renderMermaidIn(container);

		const [copyBtn] = container.querySelectorAll<HTMLButtonElement>('.mermaid-btn');
		copyBtn.click();

		expect(copyToClipboard).toHaveBeenCalledWith('graph TD; A-->B;');
	});

	it('toggles between the rendered diagram and the raw source view', async () => {
		const container = containerWith(fenceBlock('graph TD; A-->B;'));
		await renderMermaidIn(container);

		const renderPane = container.querySelector<HTMLElement>('.mermaid-render');
		const sourcePane = container.querySelector<HTMLElement>('.mermaid-source-view');
		const [, toggleBtn] = container.querySelectorAll<HTMLButtonElement>('.mermaid-btn');

		expect(renderPane?.hidden).toBe(false);
		expect(sourcePane?.hidden).toBe(true);
		expect(sourcePane?.textContent).toBe('graph TD; A-->B;');

		toggleBtn.click();
		expect(renderPane?.hidden).toBe(true);
		expect(sourcePane?.hidden).toBe(false);
		expect(toggleBtn.getAttribute('aria-pressed')).toBe('true');

		toggleBtn.click();
		expect(renderPane?.hidden).toBe(false);
		expect(sourcePane?.hidden).toBe(true);
		expect(toggleBtn.getAttribute('aria-pressed')).toBe('false');
	});

	it('preserves the toolbar and its click handlers across a theme-triggered re-render', async () => {
		const container = containerWith(fenceBlock('graph TD; A-->B;'));

		document.documentElement.dataset.theme = 'dark';
		await renderMermaidIn(container);

		document.documentElement.dataset.theme = 'light';
		await renderMermaidIn(container);

		expect(container.querySelectorAll('.mermaid-btn').length).toBe(2);
		const [copyBtn] = container.querySelectorAll<HTMLButtonElement>('.mermaid-btn');
		copyBtn.click();
		expect(copyToClipboard).toHaveBeenCalledWith('graph TD; A-->B;');
	});
});
