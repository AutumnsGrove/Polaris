// Drags a bottom-sheet panel down to dismiss it — the standard mobile
// affordance for a sheet's grab handle. Scoped to the handle node itself
// (not the whole panel) so it never competes with native scrolling of long
// content inside the sheet; see the .sheet-handle rules in app.css for why
// that split matters. Desktop renders the same panel centered, not as a
// sheet, so the gesture is a no-op above the 768px breakpoint the rest of
// the modal CSS already uses.
const DISMISS_DISTANCE = 90;
const DISMISS_VELOCITY = 0.5; // px/ms

export function swipeToDismiss(handle: HTMLElement, onDismiss: () => void) {
	const panel = handle.closest('.modal-panel') as HTMLElement | null;
	if (!panel) return {};

	let startY = 0;
	let startTime = 0;
	let dragging = false;

	function onPointerDown(e: PointerEvent) {
		if (!window.matchMedia('(max-width: 768px)').matches) return;
		dragging = true;
		startY = e.clientY;
		startTime = e.timeStamp;
		panel!.style.transition = 'none';
		handle.setPointerCapture(e.pointerId);
	}

	function onPointerMove(e: PointerEvent) {
		if (!dragging) return;
		const delta = Math.max(0, e.clientY - startY);
		panel!.style.transform = `translateY(${delta}px)`;
	}

	function onPointerUp(e: PointerEvent) {
		if (!dragging) return;
		dragging = false;
		const delta = Math.max(0, e.clientY - startY);
		const elapsed = Math.max(1, e.timeStamp - startTime);
		panel!.style.transition = '';
		panel!.style.transform = '';

		if (delta > DISMISS_DISTANCE || delta / elapsed > DISMISS_VELOCITY) {
			onDismiss();
		}
	}

	handle.addEventListener('pointerdown', onPointerDown);
	handle.addEventListener('pointermove', onPointerMove);
	handle.addEventListener('pointerup', onPointerUp);
	handle.addEventListener('pointercancel', onPointerUp);

	return {
		destroy() {
			handle.removeEventListener('pointerdown', onPointerDown);
			handle.removeEventListener('pointermove', onPointerMove);
			handle.removeEventListener('pointerup', onPointerUp);
			handle.removeEventListener('pointercancel', onPointerUp);
		}
	};
}
