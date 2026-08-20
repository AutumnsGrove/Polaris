// highlightjs-svelte ships no types (see its package.json — no "types"
// field, and DefinitelyTyped has no @types package for it either). Its
// whole public API is the one default export used in highlightjs.ts:
// a function that registers the "svelte" language on a given hljs
// instance, matching hljs's own registerLanguage(name, definitionFn)
// convention.
declare module 'highlightjs-svelte/dist/index.mjs' {
	import type { HLJSApi } from 'highlight.js';
	export default function registerSvelte(hljs: HLJSApi): void;
}
