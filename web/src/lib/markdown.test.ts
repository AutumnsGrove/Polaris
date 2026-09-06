import { describe, it, expect } from 'vitest';
import { marked } from './markdown';

describe('marked code renderer (syntax highlighting)', () => {
	it('wraps a fenced code block in hljs classes for a known language', () => {
		const html = marked.parse('```go\nfunc main() {\n\tfmt.Println("hi")\n}\n```') as string;
		expect(html).toContain('<pre><code class="hljs language-go">');
		expect(html).toContain('class="hljs-keyword">func<');
		expect(html).toContain('class="hljs-title">main<');
		expect(html).toContain('class="hljs-string">&quot;hi&quot;<');
	});

	it('renders plain (uncolored, but still escaped) code for an unlabeled fence', () => {
		// No language on the fence — deliberately not auto-detected, see the
		// doc comment in markdown.ts on why guessing was dropped after
		// testing showed it mislabeling common short snippets.
		const html = marked.parse('```\nconst x = 1;\n```') as string;
		expect(html).toContain('<pre><code class="hljs">const x = 1;</code></pre>');
	});

	it('never emits an unescaped tag from fenced code, known language or not', () => {
		const known = marked.parse('```html\n<script>alert(1)</script>\n```') as string;
		const unrecognized = marked.parse('```notarealthing\n<script>alert(1)</script>\n```') as string;
		for (const html of [known, unrecognized]) {
			expect(html).toContain('<pre><code class="hljs');
			// The raw tag must never appear un-escaped, however hljs's tokenizer
			// happens to have split it across <span> boundaries for coloring.
			expect(html).not.toContain('<script>alert(1)</script>');
			expect(html).not.toMatch(/<script(?!>alert)/);
			expect(html).toContain('&lt;');
			expect(html).toContain('&gt;');
		}
	});

	it('renders plain code, not a wrong-language guess, for an unrecognized fence tag', () => {
		const html = marked.parse('```notarealthing\nSELECT * FROM users;\n```') as string;
		expect(html).toContain('<pre><code class="hljs">SELECT * FROM users;</code></pre>');
	});

	it('colors a ```svelte fence via the highlightjs-svelte grammar', () => {
		// Svelte has no official highlight.js grammar (see highlightjs.ts's
		// doc comment) — this pins the community one actually registering
		// and producing real sub-language spans, not just silently falling
		// through to the plain-text branch above.
		const html = marked.parse('```svelte\n<script>\n  let count = 0;\n</script>\n<button>{count}</button>\n```') as string;
		expect(html).toContain('<pre><code class="hljs language-svelte">');
		expect(html).toContain('class="hljs-tag"');
		expect(html).toContain('class="hljs-keyword">let<');
	});

	it('leaves ordinary prose untouched', () => {
		const html = marked.parse('Just a **sentence**.') as string;
		expect(html).toBe('<p>Just a <strong>sentence</strong>.</p>\n');
	});
});

describe('marked code renderer (mermaid fences)', () => {
	it('wraps a ```mermaid fence in the data-mermaid discovery marker', () => {
		const html = marked.parse('```mermaid\ngraph TD;\nA-->B;\n```') as string;
		expect(html).toContain('<pre class="mermaid-source" data-mermaid>');
		expect(html).toContain('<code class="language-mermaid">');
		expect(html).toContain('graph TD;');
	});

	it('escapes mermaid source itself, since the mermaid branch skips hljs entirely', () => {
		const html = marked.parse('```mermaid\ngraph TD;\nA["<script>alert(1)</script>"]-->B;\n```') as string;
		expect(html).not.toContain('<script>alert(1)</script>');
		expect(html).toContain('&lt;script&gt;');
	});

	it('does not fire the mermaid branch for an unrelated fence tag', () => {
		const html = marked.parse('```go\nfunc main() {}\n```') as string;
		expect(html).not.toContain('data-mermaid');
	});

	it('is case-insensitive on the fence tag', () => {
		const html = marked.parse('```Mermaid\ngraph TD;\n```') as string;
		expect(html).toContain('data-mermaid');
	});
});
