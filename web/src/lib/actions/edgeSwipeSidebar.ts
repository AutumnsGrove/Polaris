import { appState } from '$lib/state.svelte';

// Drags the mobile sidebar drawer open from a left-edge swipe, or closed
// with a swipe on the drawer itself — the standard iOS/Android drawer
// gesture. Lives on the <aside> node itself (Sidebar.svelte): a *closed*
// drawer is translateX(-100%), so its own hit-test box is off-screen and
// can't receive the opening swipe — only a window-level listener can catch
// that first touch near the edge. An *open* drawer is on-screen, so
// closing swipes are handled by a normal listener on the node.
//
// Desktop's sidebar is an inline column, not an overlay drawer (see the
// @media (max-width: 768px) block in Sidebar.svelte's <style>) — the
// gesture is a no-op above that breakpoint.
const EDGE_ZONE = 24; // px from the left screen edge that can start an "open" drag
const DEADZONE = 10; // px of ambiguous movement before committing to horizontal vs. vertical
const DISMISS_DISTANCE = 70; // px
const DISMISS_VELOCITY = 0.5; // px/ms

export function edgeSwipeSidebar(node: HTMLElement) {
	let width = 280;
	let startX = 0;
	let startY = 0;
	let startTime = 0;
	let mode: 'idle' | 'pending' | 'opening' | 'closing' | 'rejected' = 'idle';
	let activePointerId: number | null = null;

	function isMobile() {
		return window.matchMedia('(max-width: 768px)').matches;
	}

	function beginDrag(e: PointerEvent, nextMode: 'opening' | 'closing') {
		mode = nextMode;
		activePointerId = e.pointerId;
		width = node.getBoundingClientRect().width || 280;
		node.style.transition = 'none';
		node.setPointerCapture(e.pointerId);
	}

	function applyDrag(dx: number) {
		const base = mode === 'opening' ? -width : 0;
		const clamped = Math.min(0, Math.max(-width, base + dx));
		node.style.transform = `translateX(${clamped}px)`;
	}

	function settle(open: boolean) {
		node.style.transition = 'transform 0.2s ease';
		node.style.transform = `translateX(${open ? 0 : -width}px)`;
		appState.sidebarOpen = open;
		// Hands control back to the class-driven CSS transform once the
		// gesture's own explicit one has finished animating, so a later
		// non-gesture toggle (the header's collapse button) isn't fighting
		// a leftover inline style.
		setTimeout(() => {
			node.style.transition = '';
			node.style.transform = '';
		}, 220);
	}

	function onPointerDown(e: PointerEvent) {
		if (e.pointerType === 'mouse' || !isMobile()) return;
		if (!appState.sidebarOpen && e.clientX > EDGE_ZONE) return;
		startX = e.clientX;
		startY = e.clientY;
		startTime = e.timeStamp;
		mode = 'pending';
	}

	function onPointerMove(e: PointerEvent) {
		if (mode === 'idle' || mode === 'rejected') return;
		if (activePointerId !== null && e.pointerId !== activePointerId) return;

		const dx = e.clientX - startX;
		const dy = e.clientY - startY;

		if (mode === 'pending') {
			if (Math.abs(dx) < DEADZONE && Math.abs(dy) < DEADZONE) return;
			if (Math.abs(dy) >= Math.abs(dx)) {
				mode = 'rejected'; // a vertical scroll, not a drawer gesture — let it through
				return;
			}
			// appState.sidebarOpen is read fresh here (not captured at
			// pointerdown) since a closing drag only ever starts on an
			// already-open drawer.
			beginDrag(e, appState.sidebarOpen ? 'closing' : 'opening');
		}

		if (mode === 'opening' || mode === 'closing') {
			e.preventDefault();
			applyDrag(dx);
		}
	}

	function onPointerUp(e: PointerEvent) {
		if (mode !== 'opening' && mode !== 'closing') {
			mode = 'idle';
			return;
		}
		const dx = e.clientX - startX;
		const elapsed = Math.max(1, e.timeStamp - startTime);
		const velocity = dx / elapsed;

		const open =
			mode === 'opening' ? dx > DISMISS_DISTANCE || velocity > DISMISS_VELOCITY : !(-dx > DISMISS_DISTANCE || -velocity > DISMISS_VELOCITY);
		settle(open);
		mode = 'idle';
		activePointerId = null;
	}

	// The opening gesture's first touch lands on whatever's underneath the
	// closed (off-screen) drawer — the main chat view, not this node — so
	// pointerdown has to be captured at the window level. Move/up stay
	// window-level too once a drag is armed, since a fast swipe can easily
	// carry the pointer off this node's now-moving bounding box.
	window.addEventListener('pointerdown', onPointerDown);
	window.addEventListener('pointermove', onPointerMove, { passive: false });
	window.addEventListener('pointerup', onPointerUp);
	window.addEventListener('pointercancel', onPointerUp);

	return {
		destroy() {
			window.removeEventListener('pointerdown', onPointerDown);
			window.removeEventListener('pointermove', onPointerMove);
			window.removeEventListener('pointerup', onPointerUp);
			window.removeEventListener('pointercancel', onPointerUp);
		}
	};
}
