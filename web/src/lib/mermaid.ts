import { copyToClipboard } from './clipboard';

// Renders `pre[data-mermaid]` blocks (see markdown.ts's code renderer) into
// live SVG diagrams. A plain post-render DOM pass, not a Svelte component —
// replacing DOM inside {@html} content doesn't compose with components, so
// this operates directly on the container ChatTurnView hands it. See
// docs/plans/mermaid-rendering.md for the full design rationale.

// Lazy module-level import — mermaid (~a few hundred KB gzipped) is only
// ever fetched by a client that actually renders a diagram, keeping it off
// the critical path of every other reply. The promise is cached so a second
// diagram in the same reply (or a later reply) doesn't re-fetch or
// re-initialize.
let mermaidPromise: ReturnType<typeof loadMermaid> | undefined;

async function loadMermaid() {
	const mod = await import('mermaid');
	return mod.default;
}

function getMermaid() {
	if (!mermaidPromise) {
		// If the dynamic import itself rejects (a transient network blip, a
		// stale chunk hash after a deploy), don't leave that rejected promise
		// cached — every later call would reuse it and mermaid rendering
		// would stay silently broken for the rest of the tab's life. Clear
		// the cache on failure so the next render attempt gets a fresh try.
		mermaidPromise = loadMermaid().catch((err) => {
			mermaidPromise = undefined;
			throw err;
		});
	}
	return mermaidPromise;
}

// Dark-by-default app ("night-sky-not-tech-neon") — 'default' is mermaid's
// light theme and would pin a bright white diagram background into an
// otherwise dark thread, so anything other than the explicit 'light' theme
// attribute maps to mermaid's 'dark' theme.
function mermaidTheme(): 'dark' | 'default' {
	if (typeof document === 'undefined') return 'dark';
	return document.documentElement.dataset.theme === 'light' ? 'default' : 'dark';
}

// Best-effort repair for the single most common real failure (confirmed
// live: see prompt.md's node-label-quoting instruction, added after hitting
// this exact parse error): an unquoted `ID[label]` node whose label
// contains punctuation mermaid's own grammar reserves for other syntax
// (parentheses, colons, pipes, `#`, braces) — mermaid reads that punctuation
// as a new token instead of label text and fails the whole diagram over one
// label. The model is told to always quote labels, but LLM instruction-
// following isn't 100%, so this backstops that rather than replacing it.
//
// Deliberately narrow: only the plain `ID[label]` node shape (not `(...)`,
// `{...}`, `((...))`, `[[...]]`, and friends) since that's the shape that's
// actually broken this way in practice — a fully general, grammar-aware
// fixer for every node shape is a lot of surface area to maintain for
// failure modes that haven't actually been observed. The character class
// excluding `[`, `]`, and `"` from the label match means an already-quoted
// label, or one using a different node shape, simply doesn't match and is
// left untouched.
const UNQUOTED_LABEL = /(^|[\s;])([A-Za-z][\w-]*)\[([^[\]"]*)\]/g;
const RISKY_PUNCTUATION = /[()#|:{}]/;

function autoQuoteLabels(source: string): string {
	return source.replace(UNQUOTED_LABEL, (match, pre: string, id: string, label: string) => {
		if (!RISKY_PUNCTUATION.test(label)) return match;
		// A literal double quote inside the label would immediately close
		// the quoted string we're about to wrap it in and re-break the
		// parse — mermaid has no in-string escape for this, so drop to a
		// plain single quote rather than leaving it broken.
		return `${pre}${id}["${label.replace(/"/g, "'")}"]`;
	});
}

// A `style NodeId fill:#f9f,stroke:#333` line customizes a node's
// background but, without an explicit `color:` property, leaves the text
// color at mermaid's theme default — confirmed live: a light custom fill
// (e.g. pale yellow) under this app's dark theme default label color
// produces near-illegible light-on-light text. This isn't a parse failure
// (mermaid renders it "successfully"), so nothing in the render/retry path
// above would ever catch it — it's a proactive readability pass, not error
// recovery, and runs unconditionally on every diagram rather than only
// after a failed render.
const STYLE_FILL_LINE = /^(\s*style\s+\S+\s+)([^\n]*\bfill:\s*#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})\b[^\n]*)$/gm;
const HAS_COLOR_PROP = /(^|,)\s*color:/;

function expandHex(hex: string): string {
	return hex.length === 3
		? hex
				.split('')
				.map((c) => c + c)
				.join('')
		: hex;
}

// Standard YIQ perceived-brightness split (not full WCAG contrast — this
// only needs to pick a legible side, not a precise contrast ratio).
function readableTextColorFor(hex: string): '#000000' | '#ffffff' {
	const full = expandHex(hex);
	const r = parseInt(full.slice(0, 2), 16);
	const g = parseInt(full.slice(2, 4), 16);
	const b = parseInt(full.slice(4, 6), 16);
	return (r * 299 + g * 587 + b * 114) / 1000 > 140 ? '#000000' : '#ffffff';
}

function ensureStyleContrast(source: string): string {
	return source.replace(STYLE_FILL_LINE, (match, prefix: string, props: string, fillHex: string) => {
		if (HAS_COLOR_PROP.test(props)) return match;
		return `${prefix}${props},color:${readableTextColorFor(fillHex)}`;
	});
}

let renderCounter = 0;

async function renderOne(mermaid: Awaited<ReturnType<typeof loadMermaid>>, source: string): Promise<string> {
	const id = `mermaid-${Date.now()}-${renderCounter++}`;
	try {
		const { svg } = await mermaid.render(id, source);
		return svg;
	} finally {
		// mermaid.render can leave a detached error node behind in the DOM
		// even on failure — clean it up so it doesn't linger invisibly.
		// Best-effort: it may not exist under every failure path, and is a
		// no-op on success once mermaid has already removed its own scratch
		// node.
		document.getElementById(id)?.remove();
	}
}

// Inline SVG markup matching the exact path data of the @lucide/svelte
// icons already used everywhere else in the chat UI (Copy, CodeXml, Check)
// — this DOM pass builds plain elements, not Svelte components, so the
// icon component itself isn't usable here, but the icons should still look
// identical to the rest of the app's chrome. width/height 13 matches the
// turn-footer's own icon buttons (ChatTurnView.svelte's copy/read-aloud/
// retry icons).
const ICON_ATTRS = 'width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"';
const COPY_ICON = `<svg ${ICON_ATTRS}><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`;
const CHECK_ICON = `<svg ${ICON_ATTRS}><path d="M20 6 9 17l-5-5"/></svg>`;
const CODE_ICON = `<svg ${ICON_ATTRS}><path d="m18 16 4-4-4-4"/><path d="m6 8-4 4 4 4"/><path d="m14.5 4-5 16"/></svg>`;

function iconButton(icon: string, title: string): HTMLButtonElement {
	const btn = document.createElement('button');
	btn.type = 'button';
	// icon-btn is app.css's plain global icon-button class (the same one
	// every other icon button in the turn footer uses) — reused here for
	// hover/transition consistency; mermaid-btn only adds what's specific
	// to sitting on top of a diagram (a translucent backdrop chip, since
	// icon-btn's transparent background assumes a plain surface behind
	// it, not arbitrary SVG diagram colors).
	btn.className = 'icon-btn mermaid-btn';
	btn.title = title;
	btn.setAttribute('aria-label', title);
	btn.innerHTML = icon;
	return btn;
}

// Builds the top-right toolbar for one rendered diagram: a copy button
// (copies the mermaid source, same text the ```mermaid fence carried, not
// the SVG markup — a viewer wants to paste/edit the diagram definition,
// not screenshot-equivalent XML) and a toggle between the rendered SVG and
// the raw source, so a reader can drop into "code block" mode the same way
// they could before this fence ever rendered as a diagram.
function buildToolbar(source: string, renderPane: HTMLElement, sourcePane: HTMLElement): HTMLDivElement {
	const toolbar = document.createElement('div');
	toolbar.className = 'mermaid-toolbar';

	const copyBtn = iconButton(COPY_ICON, 'Copy diagram source');
	copyBtn.addEventListener('click', () => {
		void copyToClipboard(source).then(() => {
			copyBtn.innerHTML = CHECK_ICON;
			setTimeout(() => {
				copyBtn.innerHTML = COPY_ICON;
			}, 1500);
		});
	});

	const toggleBtn = iconButton(CODE_ICON, 'View source');
	toggleBtn.setAttribute('aria-pressed', 'false');
	toggleBtn.addEventListener('click', () => {
		const showingSource = toggleBtn.getAttribute('aria-pressed') === 'true';
		renderPane.hidden = !showingSource;
		sourcePane.hidden = showingSource;
		toggleBtn.setAttribute('aria-pressed', showingSource ? 'false' : 'true');
		toggleBtn.title = showingSource ? 'View source' : 'View diagram';
		toggleBtn.setAttribute('aria-label', toggleBtn.title);
	});

	toolbar.append(copyBtn, toggleBtn);
	return toolbar;
}

function buildDiagramWrapper(source: string, svg: string, theme: string): HTMLDivElement {
	const wrapper = document.createElement('div');
	wrapper.className = 'mermaid-diagram';
	wrapper.dataset.mermaidSource = source;
	wrapper.dataset.mermaidTheme = theme;

	const renderPane = document.createElement('div');
	renderPane.className = 'mermaid-render';
	renderPane.innerHTML = svg;

	const sourcePane = document.createElement('pre');
	sourcePane.className = 'mermaid-source-view';
	sourcePane.hidden = true;
	const code = document.createElement('code');
	code.textContent = source;
	sourcePane.appendChild(code);

	wrapper.append(buildToolbar(source, renderPane, sourcePane), renderPane, sourcePane);
	return wrapper;
}

// Replaces every `pre[data-mermaid]` inside `container` with its rendered
// SVG, and re-renders any diagram from a previous pass whose theme no
// longer matches the current one (see ChatTurnView.svelte's $effect doc
// comment: a loaded thread's mermaid blocks render before the user's real
// theme preference finishes loading, so a later theme change/settled
// preference needs a second pass against DOM this function already
// replaced once). A block that fails to parse/render is left as its
// original `<pre>` (today's plain-code-block behavior) plus a small note —
// never a broken hole, never a throw that could take down the rest of the
// turn's render.
export async function renderMermaidIn(container: HTMLElement): Promise<void> {
	const freshBlocks = Array.from(
		container.querySelectorAll<HTMLPreElement>('pre[data-mermaid]:not([data-mermaid-failed])')
	);
	const theme = mermaidTheme();
	const staleDiagrams = Array.from(
		container.querySelectorAll<HTMLDivElement>(`.mermaid-diagram[data-mermaid-source]:not([data-mermaid-theme="${theme}"])`)
	);
	if (freshBlocks.length === 0 && staleDiagrams.length === 0) return;

	let mermaid: Awaited<ReturnType<typeof loadMermaid>>;
	try {
		mermaid = await getMermaid();
	} catch {
		// The library itself failed to load (as opposed to a single
		// diagram's source failing to parse) — every fresh block in this
		// pass is equally unable to render, not just one. Give each the same
		// visible fallback a parse error gets, rather than throwing out of
		// this function and leaving them as untouched, unexplained code
		// blocks (this async function is invoked as `void renderMermaidIn(...)`
		// from ChatTurnView.svelte, so an uncaught throw here is otherwise a
		// silent, invisible failure).
		for (const block of freshBlocks) {
			block.dataset.mermaidFailed = 'true';
			if (!block.querySelector('.mermaid-error-note')) {
				const note = document.createElement('div');
				note.className = 'mermaid-error-note';
				note.textContent = "Couldn't render this diagram — showing the source instead.";
				block.appendChild(note);
			}
		}
		return;
	}
	// Re-initialized on every pass (cheap, synchronous) rather than once at
	// load — theme is read live so a mid-session theme toggle, or the
	// settings fetch settling after a diagram's first paint, affects
	// what's on screen rather than whichever theme was active on first
	// import. securityLevel: 'strict' is the real risk here: a page's
	// fetched text echoed by the model into a diagram label is
	// attacker-controlled input reaching innerHTML, and strict mode strips
	// HTML tags from rendered labels instead of passing them through.
	mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme });

	for (const block of freshBlocks) {
		const code = block.querySelector('code');
		// The browser has already HTML-unescaped the fence's textContent for
		// us (markdown.ts escaped it only so it survives as literal text
		// inside the <code> tag) — this is the original mermaid source.
		const source = ensureStyleContrast(code?.textContent ?? '');
		try {
			const svg = await renderOne(mermaid, source);
			block.replaceWith(buildDiagramWrapper(source, svg, theme));
			continue;
		} catch {
			// Fall through to the auto-quote retry below before giving up.
		}
		const fixed = autoQuoteLabels(source);
		if (fixed !== source) {
			try {
				const svg = await renderOne(mermaid, fixed);
				// The corrected source, not the original, becomes what
				// "view source" and the copy button hand back — it's the
				// text that actually produced what's on screen, and the
				// original was, by definition, invalid mermaid anyway.
				block.replaceWith(buildDiagramWrapper(fixed, svg, theme));
				continue;
			} catch {
				// Punctuation wasn't the (only) problem — fall through to
				// the same failure note a plain parse error gets.
			}
		}
		// Parse errors are a property of the source, not the theme — mark so
		// a later theme-only pass doesn't retry a diagram that's already
		// known to fail (it never would, but it fails the same way every
		// time and there's no reason to redo the work, including the
		// auto-quote retry above). The existing note check still avoids a
		// duplicate note within this same pass.
		block.dataset.mermaidFailed = 'true';
		if (!block.querySelector('.mermaid-error-note')) {
			const note = document.createElement('div');
			note.className = 'mermaid-error-note';
			note.textContent = "Couldn't render this diagram — showing the source instead.";
			block.appendChild(note);
		}
	}

	for (const wrapper of staleDiagrams) {
		const source = wrapper.dataset.mermaidSource ?? '';
		const renderPane = wrapper.querySelector<HTMLElement>('.mermaid-render');
		if (!renderPane) continue;
		try {
			renderPane.innerHTML = await renderOne(mermaid, source);
			wrapper.dataset.mermaidTheme = theme;
		} catch {
			// A diagram that rendered fine once will keep rendering fine —
			// source doesn't change between passes. Leave the old (now
			// theme-mismatched) SVG in place rather than losing it.
		}
	}
}
