// Browser Geolocation, cached in a cookie so nearby_search gets "where the
// phone actually is" without re-prompting the user on every single message.
// A cookie (not localStorage) because it rides along automatically with any
// future plain HTTP request too, not just what state.svelte.ts reads for the
// WebSocket payload — same reasoning as this app's session/theme cookies.

const COOKIE_NAME = 'polaris_location';

// Separate cookie for a manually-typed location (Settings panel), rather
// than reusing COOKIE_NAME — the Geolocation API is unavailable outright
// over plain HTTP on a non-localhost origin (which is how this app is
// normally reached: a Tailscale IP, not https://), so there needs to be a
// way to set a location that isn't gated on that. Kept separate (not
// overwritten by primeLocation) so a manual entry survives until the user
// clears it, but getUserLocation below still prefers a real GPS fix
// whenever one is available — see its precedence comment.
const MANUAL_COOKIE_NAME = 'polaris_location_manual';

// 30 minutes: long enough that answering a burst of follow-up questions
// doesn't re-prompt or re-fetch, short enough that "near me" stays accurate
// for someone actually moving around (mobile is the primary surface here —
// see PRODUCT.md) rather than going stale for a whole day.
const COOKIE_MAX_AGE_SECONDS = 30 * 60;

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

// primeLocation silently asks the browser for a position and caches it.
// Called on every app load (see state.svelte.ts's connect()), but only
// actually hits the Geolocation API when the cookie has genuinely expired
// — passing `maximumAge` to getCurrentPosition only tells the browser a
// cached GPS fix is an acceptable answer, it doesn't stop the call (and
// whatever OS-level "app wants your location" indicator comes with it)
// from happening on literally every single refresh, which is what this
// guard is actually for. Never throws and never blocks the caller — it's
// fire-and-forget by design, since a missing location should degrade to
// config.yaml's default_location, not stall the app on a permission
// dialog the user might ignore.
export function primeLocation(): void {
	if (typeof navigator === 'undefined' || !navigator.geolocation) return;
	if (typeof document !== 'undefined' && readCookie(COOKIE_NAME)) return; // still fresh

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
