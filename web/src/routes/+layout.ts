// adapter-static's fallback ("build/index.html", see vite.config.ts) only
// covers routes that aren't prerendered — /t/[id] has no fixed set of ids
// to prerender at build time, so SSR must be off app-wide for it to build
// at all. The app was already effectively client-rendered (every page
// loads its data via onMount fetches, nothing was in the prerendered
// HTML), so this doesn't change what ends up on screen — just makes the
// existing behavior explicit and lets the dynamic route compile.
export const ssr = false;
