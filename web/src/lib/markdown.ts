// Single shared `marked` instance for the whole app — ChatTurnView.svelte
// (final answers) and ToolEvent.svelte (commentary) both import `marked`
// from here instead of the raw 'marked' package, so this renderer override
// only needs registering once. marked keeps its extension state on the
// module-level singleton, so a second `import { marked } from 'marked'`
// anywhere would silently miss this.
import { marked } from 'marked';
import hljs from './highlightjs';

marked.use({
	renderer: {
		code({ text, lang }) {
			// Deliberately NOT hljs.highlightAuto() for an unlabeled fence —
			// tried it first, and tested against real short snippets (the
			// common case in chat-length code blocks) it's actively wrong
			// often enough to look broken rather than helpful: across this
			// file's registered language set, `highlightAuto` calls
			// "SELECT * FROM users;" CSS, "const x = 1;" C++, and "hello
			// world" nginx config — all at the lowest possible confidence
			// score. Only coloring when the fence itself names a language
			// marked/the model recognized ("```go", "```python", ...) means
			// every highlight shown is one the source actually claimed,
			// never a guess. 'plaintext' is a real registered language
			// (see highlightjs.ts), not a magic sentinel — it's what gives
			// the escaping below without a language guess.
			const language = lang && hljs.getLanguage(lang) ? lang : 'plaintext';
			const result = hljs.highlight(text, { language });
			const cls = language === 'plaintext' ? 'hljs' : `hljs language-${language}`;
			return `<pre><code class="${cls}">${result.value}</code></pre>`;
		}
	}
});

export { marked };
