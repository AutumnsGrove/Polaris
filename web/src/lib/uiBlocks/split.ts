// Splits an answer into the pieces ChatTurnView renders differently: ordinary
// Markdown (the existing marked -> DOMPurify -> citations pipeline), `ui`
// fences (Svelte components) and `mermaid` fences (a stateful streaming
// diagram). See docs/plans/intelligent-ui.md "Architecture (frontend)".
//
// Why this exists at all: the answer used to be one string injected as a
// single {@html} that was re-set on every token. Svelte components can't live
// inside {@html}, and re-setting it would wipe any block's local state
// (a ticked checkbox, an in-flight diagram render) on the next token.
// Pure and DOM-free so it can be property-tested against every prefix.

export type FenceKind = 'ui' | 'mermaid';

export type Segment =
	| { kind: 'md'; text: string }
	| {
			kind: FenceKind;
			/** Fence body. While `closed` is false the last line may be a partial one (no trailing newline). */
			src: string;
			/** True once a closing fence line exists, or the turn is no longer streaming (a cut-off fence is final). */
			closed: boolean;
	  };

// CommonMark: an opening fence is 3+ backticks or tildes, then an info string
// (which may not contain a backtick for a backtick fence). Column 0 only: an
// indented fence inside a list item stays an ordinary code block, which
// markdown.ts already handles (including its own data-mermaid marker).
const FENCE = /^(`{3,}|~{3,})[ \t]*(.*?)[ \t]*$/;
const CLOSE = /^(`{3,}|~{3,})[ \t]*$/;

function classify(info: string, kinds: readonly FenceKind[]): FenceKind | null {
	if (kinds.includes('ui') && info === 'ui') return 'ui';
	// markdown.ts matches the mermaid language case-insensitively; stay in step.
	if (kinds.includes('mermaid') && info.toLowerCase() === 'mermaid') return 'mermaid';
	return null;
}

/**
 * @param kinds which fence kinds get their own segment; the rest stay in the
 * Markdown text and render as plain code blocks exactly as before.
 */
export function splitContent(
	content: string,
	streaming: boolean,
	kinds: readonly FenceKind[] = ['ui', 'mermaid']
): Segment[] {
	const segments: Segment[] = [];
	const lines = content.split('\n');
	const last = lines.length - 1;

	let md = '';
	// `char`/`len` are the opener's, so a closer must match them (CommonMark),
	// which is also what keeps a ```ui example inside a longer ```` fence inert.
	let fence: { kind: FenceKind | 'other'; char: string; len: number; body: string } | null = null;

	const flushMd = () => {
		if (md) segments.push({ kind: 'md', text: md });
		md = '';
	};

	for (let i = 0; i < lines.length; i++) {
		const line = lines[i];
		// Every line but the last has seen its newline. The last is a partial
		// line mid-stream, or '' when the content ended in a newline.
		const nl = i < last ? '\n' : '';

		if (!fence) {
			const m = FENCE.exec(line);
			// An unterminated trailing line could still grow ("```" -> "```go"),
			// so don't commit to a fence kind until its line is complete.
			if (m && (i < last || !streaming) && !(m[1][0] === '`' && m[2].includes('`'))) {
				const kind = classify(m[2], kinds);
				fence = { kind: kind ?? 'other', char: m[1][0], len: m[1].length, body: '' };
				if (kind) flushMd();
				else md += line + nl;
				continue;
			}
			md += line + nl;
			continue;
		}

		const close = CLOSE.exec(line);
		const closes = close !== null && close[1][0] === fence.char && close[1].length >= fence.len;
		if (fence.kind === 'other') {
			md += line + nl;
		} else if (closes) {
			segments.push({ kind: fence.kind, src: fence.body, closed: true });
		} else {
			fence.body += line + nl;
		}
		if (closes) fence = null;
	}

	if (fence && fence.kind !== 'other') {
		// Still open at end of input: either mid-stream, or the turn ended
		// without a closing fence (cancelled/errored), which is final.
		segments.push({ kind: fence.kind, src: fence.body, closed: !streaming });
	}
	flushMd();
	return segments;
}
