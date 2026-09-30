// Small pure helpers shared by the Atlas search page's components.

export function domainOf(url: string): string {
	try {
		return new URL(url).hostname.replace(/^www\./, '');
	} catch {
		return url;
	}
}

// Deterministic per-domain color for the favicon monogram — same spirit
// as the mockup's hand-picked colors, but generated so every domain
// gets one instead of just the handful in the sample data.
export function faviconHue(domain: string): number {
	let h = 0;
	for (let i = 0; i < domain.length; i++) h = (h * 31 + domain.charCodeAt(i)) % 360;
	return h;
}
