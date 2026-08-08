// Browser Geolocation, kept fresh by a background watchPosition() subscription
// and mirrored into a cookie so nearby_search gets "where the phone actually
// is" without re-prompting the user on every single message. A cookie (not
// localStorage) because it rides along automatically with any future plain
// HTTP request too, not just what state.svelte.ts reads for the WebSocket
// payload — same reasoning as this app's session/theme cookies.

const COOKIE_NAME = 'polaris_location';

// Separate cookie for a manually-typed location (Settings panel), rather
// than reusing COOKIE_NAME — the Geolocation API is unavailable outright
// over plain HTTP on a non-localhost origin (which is how this app is
// normally reached: a Tailscale IP, not https://), so there needs to be a
// way to set a location that isn't gated on that. Kept separate (not
// overwritten by watchLocation) so a manual entry survives until the user
// clears it, but getUserLocation below still prefers a real GPS fix
// whenever one is available — see its precedence comment.
const MANUAL_COOKIE_NAME = 'polaris_location_manual';

// The watch (see watchLocation below) keeps refreshing this cookie on its
// own for as long as the tab stays open, so this ceiling rarely matters in
// practice — it only governs how long a fix survives a closed tab, a
// revoked permission, or a GPS that's gone quiet, before getUserLocation
// gives up on it and callers fall back to config.yaml's default_location.
// A week is generous enough that closing the app overnight, or for a few
// days, doesn't lose the fix outright.
const COOKIE_MAX_AGE_SECONDS = 7 * 24 * 60 * 60;

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

let watchId: number | null = null;
let visibilityListenerAttached = false;

function handleFix(pos: GeolocationPosition): void {
	const { latitude, longitude } = pos.coords;
	setCookie(COOKIE_NAME, `${latitude}, ${longitude}`, COOKIE_MAX_AGE_SECONDS);
}

function handleFixError(): void {
	// Denied, unavailable, or timed out — leave any existing cookie alone
	// (it may still be valid) and just move on. watchPosition keeps the
	// subscription open and will fire again on its own if things recover;
	// there's nothing for us to retry here.
}

// watchLocation opens a standing subscription to the browser's location —
// the "background service" this file used to fake with a periodic
// getCurrentPosition poll. The first callback prompts for permission
// exactly like getCurrentPosition did; every callback after that is the
// browser pushing updates on its own schedule, with no further permission
// UI, so getUserLocation() below is always reading a fix from whenever the
// device last actually moved rather than one primed once and left to go
// stale for the length of the cache window. Idempotent and safe to call
// repeatedly — state.svelte.ts's connect() only calls it once, but nothing
// else here (or a hot-reloaded caller) should end up with two subscriptions
// running.
export function watchLocation(): void {
	if (typeof navigator === 'undefined' || !navigator.geolocation) return;
	if (watchId !== null) return; // already watching

	watchId = navigator.geolocation.watchPosition(handleFix, handleFixError, {
		enableHighAccuracy: false,
		maximumAge: 60_000,
		timeout: 10000
	});

	// Backgrounding the tab pauses the subscription rather than leaving it
	// running unseen — mobile is the primary surface here (see PRODUCT.md),
	// and there's no reason to keep the GPS warm for a tab nobody's looking
	// at. getUserLocation keeps serving the last fix from the cookie in the
	// meantime; coming back to the tab resumes watching immediately, which
	// re-primes it (silently — permission is already granted) rather than
	// leaving it to drift until the next natural GPS update.
	if (typeof document !== 'undefined' && !visibilityListenerAttached) {
		visibilityListenerAttached = true;
		document.addEventListener('visibilitychange', () => {
			if (document.hidden) {
				if (watchId !== null) {
					navigator.geolocation.clearWatch(watchId);
					watchId = null;
				}
			} else {
				watchLocation();
			}
		});
	}
}
