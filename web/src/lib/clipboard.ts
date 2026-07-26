// navigator.clipboard.writeText requires a "secure context" (HTTPS, or
// localhost) — Polaris is commonly reached over plain HTTP via a
// Tailscale IP (e.g. http://100.x.x.x:8899), which is NOT a secure
// context, so that API is either undefined or silently rejects there.
// Falls back to the older execCommand('copy') path, which works in any
// context as long as it's called from within a real user gesture (a
// click handler, same as this always is).
export async function copyToClipboard(text: string): Promise<void> {
	if (navigator.clipboard && window.isSecureContext) {
		await navigator.clipboard.writeText(text);
		return;
	}

	const textarea = document.createElement('textarea');
	textarea.value = text;
	// Off-screen, not display:none — some browsers refuse to select()
	// an element that isn't actually rendered.
	textarea.style.position = 'fixed';
	textarea.style.top = '0';
	textarea.style.left = '0';
	textarea.style.opacity = '0';
	document.body.appendChild(textarea);
	textarea.focus();
	textarea.select();
	try {
		if (!document.execCommand('copy')) {
			throw new Error('execCommand("copy") returned false');
		}
	} finally {
		document.body.removeChild(textarea);
	}
}
