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
// overwritten by refreshLocation) so a manual entry survives until the user
// clears it, but getUserLocation below still prefers a real GPS fix
// whenever one is available — see its precedence comment.
const MANUAL_COOKIE_NAME = 'polaris_location_manual';

// How long a fix is trusted before refreshLocation() (see below) bothers
// the GPS again. Deliberately generous — refreshLocation only ever runs
// when a message is actually being sent, never on a timer or in the
// background, so this is purely a ceiling on how stale "near me" is
// allowed to get across a burst of messages, not a battery knob.
const COOKIE_MAX_AGE_SECONDS = 7 * 24 * 60 * 60;

// How long a fix is treated as "fresh enough to skip a new GPS request",
// separate from COOKIE_MAX_AGE_SECONDS above — that one bounds when
// getUserLocation gives up on a fix entirely, this one bounds how often
// refreshLocation actually wakes the hardware. 5 minutes covers a normal
// back-and-forth conversation with a single fix.
const REFRESH_INTERVAL_SECONDS = 5 * 60;

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

// Tracks the last time refreshLocation actually asked the hardware, purely
// in memory (resets on reload, which is fine — a fresh page load starting
// a new "burst" of messages off a clean slate is the right behavior). Not
// a cookie: this is a rate limit on GPS calls, not user-facing state.
let lastFetchAttemptMs: number | null = null;

// refreshLocation silently asks the browser for a position and caches it.
// Called from dispatch() (see state.svelte.ts) right as a message goes
// out — never on a timer, never in the background — so the GPS is only
// ever touched while the app is actively being used, not for as long as
// the tab happens to be open. That's a deliberate trade against always
// having the freshest possible fix: the message that triggers a refresh
// is sent with whatever was already cached, and only the *next* one
// benefits from the new fix, but nothing runs at all while the user is
// just sitting on the page. Rate-limited by REFRESH_INTERVAL_SECONDS so a
// rapid back-and-forth doesn't hit the hardware on every single message.
// Never throws and never blocks the caller — fire-and-forget by design,
// since a missing location should degrade to config.yaml's
// default_location, not stall a message send on a permission dialog.
export function refreshLocation(): void {
	if (typeof navigator === 'undefined' || !navigator.geolocation) return;

	const now = Date.now();
	if (lastFetchAttemptMs !== null && now - lastFetchAttemptMs < REFRESH_INTERVAL_SECONDS * 1000) {
		return;
	}
	lastFetchAttemptMs = now;

	navigator.geolocation.getCurrentPosition(
		(pos) => {
			const { latitude, longitude } = pos.coords;
			setCookie(COOKIE_NAME, `${latitude}, ${longitude}`, COOKIE_MAX_AGE_SECONDS);
		},
		() => {
			// Denied, unavailable, or timed out — leave any existing cookie
			// alone (it may still be valid) and just move on. The next
			// dispatch() past the rate limit will try again on its own.
		},
		{ maximumAge: REFRESH_INTERVAL_SECONDS * 1000, timeout: 10000 }
	);
}
