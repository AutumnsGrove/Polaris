// A minimal service worker whose only job is satisfying Chrome/Android's
// installability requirement (manifest + HTTPS + a registered fetch
// handler) — it deliberately caches nothing. Every answer this app gives
// depends on a live SearXNG/OpenRouter round trip, so serving anything
// from a cache instead of the network would be a correctness bug, not
// an optimization. SvelteKit auto-registers this file for us; iOS Safari
// doesn't require it at all for "Add to Home Screen", and Chrome simply
// won't offer an install prompt over plain HTTP (Tailscale's default) —
// see README's Tailscale Serve note for getting a real HTTPS URL.
self.addEventListener('fetch', () => {});
