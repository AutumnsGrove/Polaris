import { copyToClipboard } from './clipboard';

// Renders `pre[data-mermaid]` blocks (see markdown.ts's code renderer) into
// live SVG diagrams. A plain post-render DOM pass, not a Svelte component —
// replacing DOM inside {@html} content doesn't compose with components, so
// this operates directly on the container ChatTurnView hands it. See
// docs/plans/mermaid-rendering.md for the full design rationale.

// Diagrams are drawn by Pele (obsidianmd/pele), a Mermaid-syntax-compatible
// renderer, not mermaid.js: ~230 kB gzipped with no dependencies against
// mermaid's ~1.6 MB, and it re-lays a diagram out for the width it is given
// (a wide `flowchart LR` stacks vertically on a phone instead of shrinking
// to an unreadable strip, which is what mermaid did). The model still writes
// ```mermaid fences — only the renderer changed.
//
// Lazy module-level import — the library is only ever fetched by a client
// that actually renders a diagram, keeping it off the critical path of every
// other reply. The promise is cached so a second diagram in the same reply
// (or a later reply) doesn't re-fetch.
type Pele = typeof import('pele');
let pelePromise: Promise<Pele> | undefined;
// Set once the import has resolved, so handlers that run long after the first
// render (lightbox open, resize re-layout) can render synchronously instead of
// bouncing through a promise for a module that is certainly already there.
let peleLoaded: Pele | undefined;

function getPele(): Promise<Pele> {
	if (!pelePromise) {
		// If the dynamic import itself rejects (a transient network blip, a
		// stale chunk hash after a deploy), don't leave that rejected promise
		// cached — every later call would reuse it and diagram rendering
		// would stay silently broken for the rest of the tab's life. Clear
		// the cache on failure so the next render attempt gets a fresh try.
		pelePromise = import('pele').then(
			(mod) => (peleLoaded = mod),
			(err) => {
				pelePromise = undefined;
				throw err;
			}
		);
	}
	return pelePromise;
}

// Theming is pure CSS, not a render-time option: Pele emits `var(--pele-*)`
// and ChatTurnView.svelte maps those onto the app's own --color-* tokens, so
// a theme change repaints existing diagrams with no re-render (mermaid needed
// a stale-theme pass for exactly this).

// Width a diagram should lay itself out for. Pele adapts its layout to this
// (the reason it's passed at all); 680 is the chat column's desktop width
// and only a fallback for a host that hasn't been laid out yet.
const FALLBACK_WIDTH = 680;

function layoutWidth(host: Element): number {
	const w = Math.floor(host.getBoundingClientRect().width);
	return w > 0 ? w : FALLBACK_WIDTH;
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
// background but, without an explicit `color:` property, leaves the label at
// the theme's text color (--pele-fg) — confirmed live against pele: a dark
// custom fill under the light theme renders dark-on-dark, and a light fill
// under the dark theme light-on-light, both near-illegible. Pele does no
// contrast correction of its own. This isn't a parse failure (it renders
// "successfully"), so nothing in the render/retry path would ever catch it —
// it's a proactive readability pass, not error recovery, and runs
// unconditionally on every diagram rather than only after a failed render.
// Picking the text color from the fill (not the theme) also makes the result
// theme-independent, which matters now that a theme switch doesn't re-render.
const STYLE_FILL_LINE = /^(\s*style\s+\S+\s+)([^\n]*\bfill:\s*#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})\b[^\n]*)$/gm;
// `classDef className fill:#f5f5f0,stroke:#ccc` is the same problem for a
// whole class of nodes (mermaid.js was worse here, putting the fill on the
// label too so it vanished outright). Same fix, different directive.
const CLASSDEF_FILL_LINE = /^(\s*classDef\s+\S+\s+)([^\n]*\bfill:\s*#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})\b[^\n]*)$/gm;
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

function addContrastColor(match: string, prefix: string, props: string, fillHex: string): string {
	if (HAS_COLOR_PROP.test(props)) return match;
	return `${prefix}${props},color:${readableTextColorFor(fillHex)}`;
}

function ensureStyleContrast(source: string): string {
	return source.replace(STYLE_FILL_LINE, addContrastColor).replace(CLASSDEF_FILL_LINE, addContrastColor);
}

let renderCounter = 0;

// links/images off: the source is model-written text that may echo a fetched
// page, and a diagram has no business making network requests or navigating —
// the same stance `securityLevel: 'strict'` took under mermaid. Pele never
// emits scripts, event handlers or HTML labels regardless. Throws a
// PeleError (syntax/unsupported-diagram/limit) on anything it can't draw.
function renderOne(pele: Pele, source: string, width?: number): string {
	const id = `pele-${Date.now()}-${renderCounter++}`;
	return pele.render(source, { idPrefix: id, maxWidth: width, responsive: width !== undefined, links: false, images: false }).svg;
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
// Slightly larger than the inline toolbar's 13px — the lightbox toolbar
// sits over a full-screen scrim rather than a compact chip, so its buttons
// can afford (and, on a phone, need) a bigger touch target.
const LIGHTBOX_ICON_ATTRS = 'width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"';
const LIGHTBOX_COPY_ICON = `<svg ${LIGHTBOX_ICON_ATTRS}><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`;
const LIGHTBOX_CHECK_ICON = `<svg ${LIGHTBOX_ICON_ATTRS}><path d="M20 6 9 17l-5-5"/></svg>`;
const LIGHTBOX_CODE_ICON = `<svg ${LIGHTBOX_ICON_ATTRS}><path d="m18 16 4-4-4-4"/><path d="m6 8-4 4 4 4"/><path d="m14.5 4-5 16"/></svg>`;
const LIGHTBOX_CLOSE_ICON = `<svg ${LIGHTBOX_ICON_ATTRS}><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>`;

function iconButton(icon: string, title: string, className = 'icon-btn mermaid-btn'): HTMLButtonElement {
	const btn = document.createElement('button');
	btn.type = 'button';
	// icon-btn is app.css's plain global icon-button class (the same one
	// every other icon button in the turn footer uses) — reused here for
	// hover/transition consistency; mermaid-btn only adds what's specific
	// to sitting on top of a diagram (a translucent backdrop chip, since
	// icon-btn's transparent background assumes a plain surface behind
	// it, not arbitrary SVG diagram colors).
	btn.className = className;
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
//
// getSource, not a captured string: a streaming diagram (mountMermaidStream)
// keeps this same wrapper while its source grows line by line, so the copy
// button has to read the current source at click time.
function buildToolbar(getSource: () => string, renderPane: HTMLElement, sourcePane: HTMLElement): HTMLDivElement {
	const toolbar = document.createElement('div');
	toolbar.className = 'mermaid-toolbar';

	const copyBtn = iconButton(COPY_ICON, 'Copy diagram source');
	copyBtn.addEventListener('click', () => {
		void copyToClipboard(getSource()).then(() => {
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

// --- Lightbox ---------------------------------------------------------
//
// A diagram rendered inline is necessarily small (it has to fit the chat
// column) and, on a touch device, pinch-to-zoom on the page itself just
// zooms the whole app UI rather than the diagram — there's no way to get
// a bigger look at it short of switching to the source view, which
// defeats the point of rendering it at all. This gives every diagram a
// tap target that opens it full-screen with real, isolated pinch/wheel
// zoom and drag-to-pan, the same "tap an image to see it bigger"
// convention a chat UI's image attachments already get elsewhere in the
// app — see ImageGallery.svelte's own lightbox for the sibling pattern
// this deliberately doesn't share code with (that one lightboxes a
// raster <img>; this one needs pan/zoom math against a live SVG's
// natural size instead of a fixed pixel size, and reuses this module's
// existing copy/view-source toolbar affordances, so a shared component
// would need to abstract over both anyway for one call site each).
//
// Built once, lazily, and reused for every diagram — there's only ever
// one lightbox open at a time, so a single pooled overlay avoids
// rebuilding this DOM (and re-attaching its gesture listeners) per
// diagram, per open.

interface LightboxEls {
	backdrop: HTMLDivElement;
	viewport: HTMLDivElement;
	stage: HTMLDivElement;
	renderHost: HTMLDivElement;
	sourceHost: HTMLPreElement;
	sourceCode: HTMLElement;
	toggleBtn: HTMLButtonElement;
}

let lightbox: LightboxEls | undefined;

// The mermaid source of whichever diagram the lightbox currently has
// open — a plain mutable ref rather than a function parameter baked into
// a closure, so the toolbar's copy/toggle listeners can be attached once
// in ensureLightbox and just read whatever this points at, instead of
// needing to be torn down and re-attached (or accumulate a new listener)
// on every open of a different diagram.
let currentSource = '';

// Pan/zoom state, reset on every open. transform-origin is pinned to the
// stage's own top-left (see the CSS below) so tx/ty can be plain "stage
// top-left position within the viewport" pixel offsets — the standard
// image-viewer zoom-anchored-at-a-point math (see zoomAt below) only
// stays simple when the origin isn't also moving as scale changes.
let scale = 1;
let tx = 0;
let ty = 0;
const MIN_SCALE = 1;
const MAX_SCALE = 6;

function applyTransform() {
	if (!lightbox) return;
	lightbox.stage.style.transform = `translate(${tx}px, ${ty}px) scale(${scale})`;
}

// Clamps tx/ty so the diagram can't be dragged entirely out of view —
// centers the axis instead of clamping to an edge when the scaled content
// is smaller than the viewport on that axis (otherwise a partially
// zoomed-out drag would leave it pinned off-center against one wall).
function clampPan() {
	if (!lightbox) return;
	const vp = lightbox.viewport.getBoundingClientRect();
	const contentW = lightbox.stage.offsetWidth * scale;
	const contentH = lightbox.stage.offsetHeight * scale;
	tx = contentW <= vp.width ? (vp.width - contentW) / 2 : Math.min(0, Math.max(vp.width - contentW, tx));
	ty = contentH <= vp.height ? (vp.height - contentH) / 2 : Math.min(0, Math.max(vp.height - contentH, ty));
}

// Zooms to `next` while keeping the content point currently under
// (clientX, clientY) fixed on screen — the anchor a pinch or a scroll-
// wheel zoom needs to feel like it's zooming "into" where the pointer
// is, not just rescaling around the diagram's center.
function zoomAt(clientX: number, clientY: number, next: number) {
	if (!lightbox) return;
	const vp = lightbox.viewport.getBoundingClientRect();
	const x = clientX - vp.left;
	const y = clientY - vp.top;
	const clamped = Math.min(MAX_SCALE, Math.max(MIN_SCALE, next));
	// Content-space point under the cursor, in unscaled stage pixels.
	const px = (x - tx) / scale;
	const py = (y - ty) / scale;
	scale = clamped;
	tx = x - px * scale;
	ty = y - py * scale;
	clampPan();
	applyTransform();
}

function resetZoom() {
	scale = MIN_SCALE;
	tx = 0;
	ty = 0;
	clampPan();
	applyTransform();
}

// Pan/pinch via Pointer Events (unifies mouse/touch/pen) rather than
// separate mouse + touch listeners — a pinch is just "two active
// pointers," so one Map of in-flight pointers covers both one-finger
// drag-to-pan and two-finger pinch-to-zoom without duplicating the
// tracking logic. touch-action: none on the viewport (see CSS) is what
// stops the browser's own page-pinch-zoom from ever seeing these touches
// — that's the actual fix for "pinching shifts the whole UI," not
// anything in this handler itself.
function wirePanZoom(viewport: HTMLDivElement) {
	const pointers = new Map<number, { x: number; y: number }>();
	// Baseline captured at the start of a drag or a pinch: `dist` for a
	// pinch's scale ratio, and `mid`/`content` as the anchor pair a pinch
	// needs to zoom "into" the pinch center rather than the diagram's
	// center — `content` is the stage-space point that was under `mid`
	// when the gesture began (computed the same way zoomAt derives it),
	// and every subsequent frame re-solves tx/ty so that same content
	// point stays under the pinch's *current* midpoint at the *current*
	// scale. (A first version of this just added the midpoint's on-screen
	// delta to tx/ty without ever touching the anchor for the new scale —
	// it panned correctly but the zoom itself drifted away from wherever
	// the fingers actually were, confirmed live via a simulated pinch
	// that left the diagram scrolled entirely out of view.) A one-finger
	// drag reuses the same shape with dist unused.
	let gesture: { dist: number; mid: { x: number; y: number }; content: { x: number; y: number }; scale: number } | null = null;

	function midpoint(): { x: number; y: number } {
		const pts = Array.from(pointers.values());
		return { x: (pts[0].x + pts[1].x) / 2, y: (pts[0].y + pts[1].y) / 2 };
	}
	function distance(): number {
		const pts = Array.from(pointers.values());
		return Math.hypot(pts[0].x - pts[1].x, pts[0].y - pts[1].y);
	}
	// Stage-space point currently under viewport-relative point `mid`, at
	// the current scale/tx/ty — the inverse of zoomAt's own px/py.
	function contentUnder(mid: { x: number; y: number }): { x: number; y: number } {
		if (!lightbox) return { x: 0, y: 0 };
		const vp = lightbox.viewport.getBoundingClientRect();
		return { x: (mid.x - vp.left - tx) / scale, y: (mid.y - vp.top - ty) / scale };
	}
	function beginGesture(): void {
		const mid = pointers.size === 2 ? midpoint() : Array.from(pointers.values())[0];
		gesture = { dist: pointers.size === 2 ? distance() : 0, mid, content: contentUnder(mid), scale };
	}

	viewport.addEventListener('pointerdown', (e) => {
		viewport.setPointerCapture(e.pointerId);
		pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
		beginGesture();
	});

	viewport.addEventListener('pointermove', (e) => {
		if (!pointers.has(e.pointerId) || !lightbox) return;
		pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
		if (!gesture) return;
		const vp = lightbox.viewport.getBoundingClientRect();
		if (pointers.size === 2) {
			const mid = midpoint();
			const ratio = distance() / (gesture.dist || 1);
			scale = Math.min(MAX_SCALE, Math.max(MIN_SCALE, gesture.scale * ratio));
			tx = mid.x - vp.left - gesture.content.x * scale;
			ty = mid.y - vp.top - gesture.content.y * scale;
			clampPan();
			applyTransform();
		} else if (pointers.size === 1 && scale > MIN_SCALE) {
			const p = pointers.get(e.pointerId)!;
			tx = p.x - vp.left - gesture.content.x * scale;
			ty = p.y - vp.top - gesture.content.y * scale;
			clampPan();
			applyTransform();
		}
	});

	function release(e: PointerEvent) {
		pointers.delete(e.pointerId);
		// Re-baseline instead of clearing outright: lifting one finger of a
		// two-finger pinch should fall through to a plain one-finger pan of
		// whatever's left, not stop responding until the pointer is lifted
		// and pressed again.
		if (pointers.size > 0) beginGesture();
		else gesture = null;
	}
	viewport.addEventListener('pointerup', release);
	viewport.addEventListener('pointercancel', release);

	// Wheel: desktop trackpad/mouse zoom. Every browser reports a pinch-
	// to-zoom trackpad gesture as a wheel event with ctrlKey set (that's
	// the actual OS gesture translated to a "zoom" wheel event, not a
	// held-down Ctrl key), but a plain mouse wheel over the lightbox
	// should zoom too rather than doing nothing, so this doesn't gate on
	// ctrlKey — there's no scroll position for wheel to otherwise mean.
	viewport.addEventListener(
		'wheel',
		(e) => {
			e.preventDefault();
			const factor = Math.exp(-e.deltaY * 0.01);
			zoomAt(e.clientX, e.clientY, scale * factor);
		},
		{ passive: false }
	);

	// Double-click/double-tap toggles between fit (1x) and a fixed 2.5x,
	// anchored at the click point — 'dblclick' fires for both a mouse
	// double-click and a touch double-tap in every browser tested here
	// (Safari/Chrome), so one listener covers both input types.
	viewport.addEventListener('dblclick', (e) => {
		zoomAt(e.clientX, e.clientY, scale > MIN_SCALE ? MIN_SCALE : 2.5);
	});
}

function closeLightbox() {
	if (!lightbox) return;
	lightbox.backdrop.hidden = true;
	document.body.style.overflow = '';
}

// Lazily builds the single pooled lightbox and appends it to <body> (not
// the diagram's own container) — a full-screen overlay has to escape
// .prose's layout/overflow entirely, the same reason a modal never lives
// inside the content that opens it. Reuses app.css's .modal-backdrop /
// .modal-backdrop-close (dim + blur + click-outside-to-dismiss) exactly
// the way ImageLightbox.svelte does for the photo lightbox — one scrim
// convention for "something opened full-screen over the app," not a
// second one invented here. Everything past the scrim (the pannable
// viewport, the floating toolbar) is specific to this lightbox's own
// content and gets its own mermaid-lightbox-* styling in app.css, next
// to ImageLightbox's own .lightbox-* rules.
function ensureLightbox(): LightboxEls {
	if (lightbox) return lightbox;

	const backdrop = document.createElement('div');
	backdrop.className = 'modal-backdrop mermaid-lightbox-backdrop';
	backdrop.hidden = true;

	const dismissBtn = document.createElement('button');
	dismissBtn.className = 'modal-backdrop-close';
	dismissBtn.setAttribute('aria-label', 'Close');
	dismissBtn.addEventListener('click', closeLightbox);

	const content = document.createElement('div');
	content.className = 'mermaid-lightbox-content';

	const viewport = document.createElement('div');
	viewport.className = 'mermaid-lightbox-viewport';

	const stage = document.createElement('div');
	stage.className = 'mermaid-lightbox-stage';

	const renderHost = document.createElement('div');
	renderHost.className = 'mermaid-lightbox-render';

	const sourceHost = document.createElement('pre');
	sourceHost.className = 'mermaid-lightbox-source';
	sourceHost.hidden = true;
	const sourceCode = document.createElement('code');
	sourceHost.appendChild(sourceCode);

	stage.append(renderHost, sourceHost);
	viewport.appendChild(stage);

	const toolbar = document.createElement('div');
	toolbar.className = 'mermaid-lightbox-toolbar';

	const copyBtn = iconButton(LIGHTBOX_COPY_ICON, 'Copy diagram source', 'mermaid-lightbox-btn');
	copyBtn.addEventListener('click', () => {
		void copyToClipboard(currentSource).then(() => {
			copyBtn.innerHTML = LIGHTBOX_CHECK_ICON;
			setTimeout(() => {
				copyBtn.innerHTML = LIGHTBOX_COPY_ICON;
			}, 1500);
		});
	});

	const toggleBtn = iconButton(LIGHTBOX_CODE_ICON, 'View source', 'mermaid-lightbox-btn');
	toggleBtn.setAttribute('aria-pressed', 'false');
	toggleBtn.addEventListener('click', () => {
		if (!lightbox) return;
		const showingSource = toggleBtn.getAttribute('aria-pressed') === 'true';
		lightbox.renderHost.hidden = !showingSource;
		lightbox.sourceHost.hidden = showingSource;
		toggleBtn.setAttribute('aria-pressed', showingSource ? 'false' : 'true');
		toggleBtn.title = showingSource ? 'View source' : 'View diagram';
		toggleBtn.setAttribute('aria-label', toggleBtn.title);
	});

	const closeBtn = iconButton(LIGHTBOX_CLOSE_ICON, 'Close preview', 'mermaid-lightbox-btn');
	closeBtn.addEventListener('click', closeLightbox);
	toolbar.append(copyBtn, toggleBtn, closeBtn);

	content.append(viewport, toolbar);
	backdrop.append(dismissBtn, content);
	document.body.appendChild(backdrop);

	wirePanZoom(viewport);

	document.addEventListener('keydown', (e) => {
		if (e.key === 'Escape' && !backdrop.hidden) closeLightbox();
	});

	lightbox = { backdrop, viewport, stage, renderHost, sourceHost, sourceCode, toggleBtn };
	return lightbox;
}

function openLightbox(source: string, svg: string) {
	const els = ensureLightbox();
	currentSource = source;
	els.renderHost.innerHTML = svg;
	els.sourceCode.textContent = source;
	els.renderHost.hidden = false;
	els.sourceHost.hidden = true;
	els.toggleBtn.setAttribute('aria-pressed', 'false');
	els.toggleBtn.title = 'View source';
	els.toggleBtn.setAttribute('aria-label', 'View source');

	resetZoom();
	els.backdrop.hidden = false;
	// Belt-and-suspenders alongside the backdrop's own fixed positioning:
	// iOS Safari can still rubber-band/scroll the page behind a fixed
	// overlay on a swipe that starts outside any of this handler's own
	// listeners, so pin the body too for as long as the lightbox is open.
	document.body.style.overflow = 'hidden';
}

// Re-lays a diagram out when its container's width changes (a phone rotating,
// a sidebar opening) — Pele picks the layout from the width, so a one-time
// render would stay in whatever shape the first width produced. rAF-coalesced
// so a drag-resize doesn't render per pixel. Reads the source live from the
// wrapper for the same reason the click handler does.
function observeWidth(wrapper: HTMLDivElement, renderPane: HTMLElement): void {
	if (typeof ResizeObserver === 'undefined') return;
	let lastWidth = layoutWidth(wrapper);
	let frame = 0;
	new ResizeObserver(() => {
		if (frame) return;
		frame = requestAnimationFrame(() => {
			frame = 0;
			const width = layoutWidth(wrapper);
			if (width === lastWidth || !wrapper.isConnected) return;
			lastWidth = width;
			// A wrapper only exists after a successful render, so the module
			// is loaded by the time this fires.
			if (!peleLoaded) return;
			try {
				renderPane.innerHTML = renderOne(peleLoaded, wrapper.dataset.mermaidSource ?? '', width);
			} catch {
				// The source rendered at the first width; keep that SVG
				// rather than blanking the diagram over a resize.
			}
		});
	}).observe(wrapper);
}

function buildDiagramWrapper(source: string, svg: string): HTMLDivElement {
	const wrapper = document.createElement('div');
	wrapper.className = 'mermaid-diagram';
	wrapper.dataset.mermaidSource = source;

	const renderPane = document.createElement('div');
	renderPane.className = 'mermaid-render';
	renderPane.innerHTML = svg;
	renderPane.title = 'Tap to enlarge';
	// Read live from the wrapper rather than closing over `source`/`svg`: both
	// get replaced in place (a resize re-layout, and every new line of a
	// streaming diagram), and a closure would open the lightbox on the stale
	// first render. The lightbox lays the diagram out at natural size
	// (responsive: false) instead of reusing the inline SVG: inline it is
	// fitted to the chat column (a phone gets the stacked layout), but the
	// lightbox's pan/zoom wants the full-size drawing to zoom into.
	renderPane.addEventListener('click', () => {
		const current = wrapper.dataset.mermaidSource ?? '';
		let full = renderPane.innerHTML;
		try {
			if (peleLoaded) full = renderOne(peleLoaded, current);
		} catch {
			// Fall back to what's already on screen.
		}
		openLightbox(current, full);
	});

	const sourcePane = document.createElement('pre');
	sourcePane.className = 'mermaid-source-view';
	sourcePane.hidden = true;
	const code = document.createElement('code');
	code.textContent = source;
	sourcePane.appendChild(code);

	wrapper.append(buildToolbar(() => wrapper.dataset.mermaidSource ?? '', renderPane, sourcePane), renderPane, sourcePane);
	observeWidth(wrapper, renderPane);
	return wrapper;
}

// Swaps a new render into an existing wrapper, keeping its toolbar and the
// user's view-source toggle state.
function updateDiagramWrapper(wrapper: HTMLDivElement, source: string, svg: string) {
	wrapper.dataset.mermaidSource = source;
	const renderPane = wrapper.querySelector<HTMLElement>('.mermaid-render');
	const code = wrapper.querySelector<HTMLElement>('.mermaid-source-view code');
	if (renderPane) renderPane.innerHTML = svg;
	if (code) code.textContent = source;
}

function appendErrorNote(block: HTMLElement) {
	if (block.querySelector('.mermaid-error-note')) return;
	const note = document.createElement('div');
	note.className = 'mermaid-error-note';
	note.textContent = "Couldn't render this diagram — showing the source instead.";
	block.appendChild(note);
}

// Tries the source as-is, then once more with unquoted-label punctuation
// repaired (see autoQuoteLabels — Pele's grammar fails on the same input
// mermaid's did, confirmed against the real package). Returns the source that
// actually produced the SVG: the corrected text, not the original, is what
// "view source" and copy should hand back. null means neither attempt
// rendered.
function renderWithRepair(pele: Pele, source: string, width: number): { source: string; svg: string } | null {
	try {
		return { source, svg: renderOne(pele, source, width) };
	} catch {
		// Fall through to the auto-quote retry below before giving up.
	}
	const fixed = autoQuoteLabels(source);
	if (fixed === source) return null;
	try {
		return { source: fixed, svg: renderOne(pele, fixed, width) };
	} catch {
		// Punctuation wasn't the (only) problem.
		return null;
	}
}

// Replaces every `pre[data-mermaid]` inside `container` with its rendered
// SVG. A block that fails to parse/render is left as its original `<pre>`
// (today's plain-code-block behavior) plus a small note — never a broken
// hole, never a throw that could take down the rest of the turn's render.
export async function renderMermaidIn(container: HTMLElement): Promise<void> {
	const freshBlocks = Array.from(
		container.querySelectorAll<HTMLPreElement>('pre[data-mermaid]:not([data-mermaid-failed])')
	);
	if (freshBlocks.length === 0) return;

	let pele: Pele;
	try {
		pele = await getPele();
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
			appendErrorNote(block);
		}
		return;
	}

	for (const block of freshBlocks) {
		const code = block.querySelector('code');
		// The browser has already HTML-unescaped the fence's textContent for
		// us (markdown.ts escaped it only so it survives as literal text
		// inside the <code> tag) — this is the original mermaid source.
		const rendered = renderWithRepair(pele, ensureStyleContrast(code?.textContent ?? ''), layoutWidth(block));
		if (rendered) {
			block.replaceWith(buildDiagramWrapper(rendered.source, rendered.svg));
			continue;
		}
		// Mark so a later pass doesn't retry a diagram that's already known
		// to fail (it fails the same way every time, auto-quote retry
		// included). The note check still avoids a duplicate note within
		// this same pass.
		block.dataset.mermaidFailed = 'true';
		appendErrorNote(block);
	}
}

// --- Streaming ----------------------------------------------------------
//
// The DOM pass above only ever sees a finished turn, because an UNCLOSED fence
// is briefly a half-diagram and parsing that flashes a failure note. That is a
// reason not to render an open fence, not a reason to wait for the whole turn:
// docs/plans/intelligent-ui.md "Streaming mermaid" spiked real mermaid 11 at
// every complete-line prefix (flowchart 9/9 valid, sequence 8/8, gantt 8/8, ER
// 5/8 — only inside an open `{ ... }` attribute block). So a streaming diagram
// is re-rendered on each new complete line, and a prefix that doesn't parse
// simply keeps the last good render on screen.
//
// Phone render cost is unmeasured (desktop median ~29ms under mermaid; Pele
// is sub-millisecond on its own benchmarks, unverified here); the throttle below
// is what keeps a slow device from falling behind rather than a guess at it.

// Wait this many multiples of the last render's duration between renders, so
// a slow phone renders fewer intermediate frames instead of queueing them.
const THROTTLE_FACTOR = 3;

export interface MermaidStream {
	/** `src` is the fence body; while !closed its last line may be partial. */
	update(src: string, closed: boolean): void;
	destroy(): void;
}

export function mountMermaidStream(host: HTMLElement): MermaidStream {
	let destroyed = false;
	let running = false;
	let wanted: { src: string; closed: boolean } | null = null;
	let wrapper: HTMLDivElement | undefined;
	let lastAttempt: string | null = null;
	let lastDuration = 0;
	let lastEnd = 0;
	let timer: ReturnType<typeof setTimeout> | undefined;

	function show(source: string, svg: string) {
		if (!wrapper) {
			wrapper = buildDiagramWrapper(source, svg);
			host.replaceChildren(wrapper);
		} else {
			updateDiagramWrapper(wrapper, source, svg);
		}
		// Short fade so the swap reads as growth, not a flash. Restarting the
		// CSS animation needs the class removed and a reflow in between; the
		// animation itself is switched off under prefers-reduced-motion (CSS).
		const pane = wrapper.querySelector<HTMLElement>('.mermaid-render');
		if (pane) {
			pane.classList.remove('mermaid-fade');
			void pane.offsetWidth;
			pane.classList.add('mermaid-fade');
		}
	}

	function showFailure(source: string) {
		wrapper = undefined;
		// Same shape markdown.ts emits for a fence, pre-marked as failed so the
		// ChatTurnView DOM pass doesn't retry it.
		const pre = document.createElement('pre');
		pre.className = 'mermaid-source';
		pre.dataset.mermaid = '';
		pre.dataset.mermaidFailed = 'true';
		const code = document.createElement('code');
		code.className = 'language-mermaid';
		code.textContent = source;
		pre.appendChild(code);
		appendErrorNote(pre);
		host.replaceChildren(pre);
	}

	function showPending() {
		if (host.firstChild) return;
		const note = document.createElement('div');
		note.className = 'mermaid-pending';
		note.textContent = 'Drawing diagram…';
		host.appendChild(note);
	}

	async function renderJob(text: string, closed: boolean) {
		let pele: Pele;
		try {
			pele = await getPele();
		} catch {
			// Library failed to load. Mid-stream, stay on whatever is showing;
			// at the end, fall back to the source like the DOM pass does.
			if (closed && !destroyed) showFailure(text);
			return;
		}
		if (destroyed) return;
		const width = layoutWidth(host);

		if (closed) {
			// Final render exactly as the DOM pass does it, repair included.
			const rendered = renderWithRepair(pele, ensureStyleContrast(text), width);
			if (rendered) show(rendered.source, rendered.svg);
			else showFailure(text);
			return;
		}

		// The repair runs on every prefix, not just the final one: a bad
		// label or a pale `style` fill is just as likely on line 3. render() throws on an invalid
		// prefix (checked: flowchart/sequence/ER prefixes fail exactly where
		// mermaid's parse did), which is the normal case mid-stream, not an
		// error — keep the last good render and wait for the next line.
		const prepared = ensureStyleContrast(autoQuoteLabels(text));
		try {
			show(prepared, renderOne(pele, prepared, width));
		} catch {
			// Invalid prefix; see above.
		}
	}

	async function pump() {
		timer = undefined;
		if (running || destroyed || !wanted) return;
		const job = wanted;
		// Only complete lines are parseable; the partial one is still arriving.
		const text = job.closed ? job.src : job.src.slice(0, job.src.lastIndexOf('\n') + 1);
		if (!job.closed) {
			if (!text.trim() || text === lastAttempt) {
				wanted = null;
				return;
			}
			const wait = lastEnd + THROTTLE_FACTOR * lastDuration - performance.now();
			if (wait > 0) {
				timer = setTimeout(() => void pump(), wait);
				return;
			}
		}
		wanted = null;
		lastAttempt = text;
		running = true;
		const start = performance.now();
		try {
			await renderJob(text, job.closed);
		} finally {
			running = false;
			lastEnd = performance.now();
			lastDuration = lastEnd - start;
		}
		// Latest wins: anything that arrived during the render supersedes it.
		if (wanted) void pump();
	}

	return {
		update(src, closed) {
			if (destroyed) return;
			if (!closed) showPending();
			wanted = { src, closed };
			// A closing fence must not sit behind a throttle wait.
			if (closed && timer !== undefined) {
				clearTimeout(timer);
				timer = undefined;
			}
			if (timer === undefined) void pump();
		},
		destroy() {
			destroyed = true;
			if (timer !== undefined) clearTimeout(timer);
		}
	};
}
