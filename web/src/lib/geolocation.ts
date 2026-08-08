// Browser Geolocation. The live fix itself is fetched on demand, only in
// response to the backend's "location_request" event (see state.svelte.ts's
// handleEvent and requestFreshLocation below) — never proactively, on a
// timer, or in the background, so the GPS is touched only on the turns
// that actually end up calling nearby_search or weather. The result is
// still mirrored into a cookie (not localStorage, so it rides along
// automatically with any future plain HTTP request too) purely as a
// last-known fallback sent with every message — see ClientMessage.
// UserLocation's doc comment in gateway/protocol.go for how that fallback
// tier fits under the live round trip.

const COOKIE_NAME = 'polaris_location';

// Separate cookie for a manually-typed location (Settings panel), rather
// than reusing COOKIE_NAME — the Geolocation API is unavailable outright
// over plain HTTP on a non-localhost origin (which is how this app is
// normally reached: a Tailscale IP, not https://), so there needs to be a
// way to set a location that isn't gated on that. Kept separate (not
// overwritten by requestFreshLocation) so a manual entry survives until
// the user clears it, but getUserLocation below still prefers a real GPS
// fix whenever one is available — see its precedence comment.
const MANUAL_COOKIE_NAME = 'polaris_location_manual';

// How long the last-known-fix cookie survives. Generous, since it's now
// only ever a fallback of last resort (see the file-level comment above) —
// this just bounds how long a closed tab, a revoked permission, or a GPS
// gone quiet keeps feeding a stale-but-plausible fix to the server's own
// fallback chain before getUserLocation gives up on it entirely.
const COOKIE_MAX_AGE_SECONDS = 7 * 24 * 60 * 60;

// Passed to getCurrentPosition as maximumAge: lets the browser answer
// instantly from its own cache instead of re-polling the hardware if two
// "location_request" events land within a couple minutes of each other —
// e.g. a "coffee nearby, and what's the weather" back-to-back. This file
// doesn't need its own throttle on top of that; the browser already does
// this bookkeeping.
const FRESH_ENOUGH_MS = 2 * 60 * 1000;

// 180 days: a manually-typed location is a deliberate, low-frequency
// choice (unlike a GPS fix, it doesn't drift), so it should survive across
// sessions rather than needing to be re-entered.
const MANUAL_COOKIE_MAX_AGE_SECONDS = 180 * 24 * 60 * 60;

function setCookie(name: string, value: string, maxAgeSeconds: number) {
	document.cookie = `${name}=${encodeURIComponent(value)}; max-age=${maxAgeSeconds}; path=/; samesite=lax`;
}

function readCookie(name: string): string | undefined {
	const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`));
	return match ? decodeURIComponent(match[1]) : undefined;
}

// getUserLocation returns the best available client-side location, or
// undefined if neither source is set (callers fall back to config.yaml's
// default_location in that case). A real GPS fix takes precedence over a
// manual entry whenever both are present — the manual one is a fallback
// for when the Geolocation API isn't usable at all (plain HTTP), not a
// permanent pin, so it should get out of the way automatically the moment
// a genuine fix starts coming in again.
export function getUserLocation(): string | undefined {
	if (typeof document === 'undefined') return undefined;
	return readCookie(COOKIE_NAME) ?? readCookie(MANUAL_COOKIE_NAME);
}

// getManualLocation/setManualLocation/clearManualLocation back the
// Settings panel's "My location" field — plain text (an address or city),
// not coordinates, since it's typed by a human rather than read off a GPS
// sensor.
export function getManualLocation(): string {
	if (typeof document === 'undefined') return '';
	return readCookie(MANUAL_COOKIE_NAME) ?? '';
}

export function setManualLocation(value: string): void {
	const trimmed = value.trim();
	if (!trimmed) {
		clearManualLocation();
		return;
	}
	setCookie(MANUAL_COOKIE_NAME, trimmed, MANUAL_COOKIE_MAX_AGE_SECONDS);
}

export function clearManualLocation(): void {
	setCookie(MANUAL_COOKIE_NAME, '', 0);
}

// requestFreshLocation asks the browser for a live position, right now —
// called only from state.svelte.ts's handleEvent, in direct response to
// the backend's "location_request" event. This is the one and only place
// this file ever touches navigator.geolocation: no page-load prime, no
// timer, no standing watch. If this browser has never been asked before,
// this is exactly where the real "Allow location" prompt happens — that's
// correct, not a bug, since it only happens the first time a turn actually
// needs a location at all, not on every tab open.
//
// Resolves to "" (never rejects) on denial, an unavailable API, or a
// timeout — state.svelte.ts sends that straight back to the server as-is,
// since "no location" is a normal outcome the backend already falls back
// from, not an error worth surfacing to the user.
export function requestFreshLocation(): Promise<string> {
	return new Promise((resolve) => {
		if (typeof navigator === 'undefined' || !navigator.geolocation) {
			resolve('');
			return;
		}
		navigator.geolocation.getCurrentPosition(
			(pos) => {
				const { latitude, longitude } = pos.coords;
				const loc = `${latitude}, ${longitude}`;
				setCookie(COOKIE_NAME, loc, COOKIE_MAX_AGE_SECONDS);
				resolve(loc);
			},
			() => resolve(''),
			{ maximumAge: FRESH_ENOUGH_MS, timeout: 8000 }
		);
	});
}
