// Browser Geolocation, cached in a cookie so nearby_search gets "where the
// phone actually is" without re-prompting the user on every single message.
// A cookie (not localStorage) because it rides along automatically with any
// future plain HTTP request too, not just what state.svelte.ts reads for the
// WebSocket payload — same reasoning as this app's session/theme cookies.

const COOKIE_NAME = 'polaris_location';

// 30 minutes: long enough that answering a burst of follow-up questions
// doesn't re-prompt or re-fetch, short enough that "near me" stays accurate
// for someone actually moving around (mobile is the primary surface here —
// see PRODUCT.md) rather than going stale for a whole day.
const COOKIE_MAX_AGE_SECONDS = 30 * 60;

function setCookie(name: string, value: string, maxAgeSeconds: number) {
	document.cookie = `${name}=${encodeURIComponent(value)}; max-age=${maxAgeSeconds}; path=/; samesite=lax`;
}

function readCookie(name: string): string | undefined {
	const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`));
	return match ? decodeURIComponent(match[1]) : undefined;
}

// getUserLocation returns the cached "lat, lon" string, or undefined if the
// browser never granted permission (or the cache expired) — callers should
// treat that as "no browser location available", not an error.
export function getUserLocation(): string | undefined {
	if (typeof document === 'undefined') return undefined;
	return readCookie(COOKIE_NAME);
}

// primeLocation silently asks the browser for a position and caches it.
// Safe to call on every app load: if permission was already denied, the
// browser resolves the error callback immediately with no repeat prompt; if
// it was already granted, this just refreshes the cookie. Never throws and
// never blocks the caller — it's fire-and-forget by design, since a missing
// location should degrade to config.yaml's default_location, not stall the
// app waiting on a permission dialog the user might ignore.
export function primeLocation(): void {
	if (typeof navigator === 'undefined' || !navigator.geolocation) return;

	navigator.geolocation.getCurrentPosition(
		(pos) => {
			const { latitude, longitude } = pos.coords;
			setCookie(COOKIE_NAME, `${latitude}, ${longitude}`, COOKIE_MAX_AGE_SECONDS);
		},
		() => {
			// Denied, unavailable, or timed out — leave any existing cookie
			// alone (it may still be valid) and just move on.
		},
		{ maximumAge: COOKIE_MAX_AGE_SECONDS * 1000, timeout: 10000 }
	);
}
