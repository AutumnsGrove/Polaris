import { describe, it, expect, vi } from 'vitest';

// Separate file (not a case inside mermaid.test.ts) so mermaid.ts's
// module-level `mermaidPromise` cache starts genuinely fresh — each test
// file gets its own module registry, which lets this test simulate the
// dynamic `import('mermaid')` itself rejecting on the *first* call, before
// anything else in the process has ever resolved it successfully.
//
// Regression test for a real bug found live: getMermaid() cached whatever
// promise loadMermaid() returned, including a rejected one. One transient
// import failure (chunk-load blip, stale hash after a deploy) permanently
// broke mermaid rendering for the rest of that browser tab, and the
// rejection happened before renderMermaidIn's per-block try/catch — so
// nothing rendered, no error note appeared, and the failure was invisible
// (an unhandled rejection from the unawaited `void renderMermaidIn(...)`
// call in ChatTurnView.svelte).
let importShouldFail = true;

vi.mock('mermaid', () => {
	if (importShouldFail) return Promise.reject(new Error('failed to fetch dynamically imported module'));
	return Promise.resolve({ default: { initialize, render } });
});

const initialize = vi.fn();
const render = vi.fn(async (id: string, source: string) => ({ svg: `<svg data-id="${id}">${source}</svg>` }));

const { renderMermaidIn } = await import('./mermaid');

function fenceBlock(source: string): HTMLElement {
	const pre = document.createElement('pre');
	pre.setAttribute('data-mermaid', '');
	const code = document.createElement('code');
	code.className = 'language-mermaid';
	code.textContent = source;
	pre.appendChild(code);
	return pre;
}

describe('renderMermaidIn when the mermaid library itself fails to load', () => {
	it('shows the fallback note instead of silently doing nothing, then recovers on a later pass', async () => {
		const container = document.createElement('div');
		container.appendChild(fenceBlock('graph TD; A-->B;'));

		await renderMermaidIn(container);

		expect(render).not.toHaveBeenCalled();
		expect(container.querySelector('.mermaid-error-note')).not.toBeNull();
		expect(container.querySelector('pre[data-mermaid]')).not.toBeNull();

		// The library becomes loadable again (e.g. the network blip passed) —
		// the earlier rejection must not have been cached forever.
		importShouldFail = false;
		const retry = document.createElement('div');
		retry.appendChild(fenceBlock('graph TD; C-->D;'));
		await renderMermaidIn(retry);

		expect(render).toHaveBeenCalledTimes(1);
		expect(retry.querySelector('.mermaid-diagram svg')).not.toBeNull();
	});
});
