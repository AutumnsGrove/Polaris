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

	it('leaves ordinary prose untouched', () => {
		const html = marked.parse('Just a **sentence**.') as string;
		expect(html).toBe('<p>Just a <strong>sentence</strong>.</p>\n');
	});
});
