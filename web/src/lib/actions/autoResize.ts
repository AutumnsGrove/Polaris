// Grows a textarea to fit its content (up to maxHeight, then scrolls) —
// used by both the main composer and the message-edit box so neither ever
// squeezes multi-line text into a fixed-height box. value is threaded
// through as part of the action's argument (not just read via the 'input'
// event) so an external reset — e.g. the composer clearing itself after
// submit — collapses the height back down too, not just user typing.
export function autoResize(node: HTMLTextAreaElement, params: { value: string; maxHeight?: number }) {
	function resize() {
		node.style.height = 'auto';
		node.style.height = Math.min(node.scrollHeight, params.maxHeight ?? 240) + 'px';
	}

	resize();
	node.addEventListener('input', resize);

	return {
		update(newParams: { value: string; maxHeight?: number }) {
			params = newParams;
			resize();
		},
		destroy() {
			node.removeEventListener('input', resize);
		}
	};
}
