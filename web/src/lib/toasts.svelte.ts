export class ToastState {
	// Brief, app-level confirmation banners — first use is the copy
	// buttons in ChatTurnView.svelte, where the per-button checkmark swap
	// alone turned out to not be a clear enough "yes, that worked" signal
	// on its own. A plain array (not a single "current toast") so two
	// quick actions don't cut each other off mid-fade.
	toasts = $state<{ id: number; message: string }[]>([]);
	private nextId = 0;

	show(message: string, durationMs = 2000) {
		const id = this.nextId++;
		this.toasts = [...this.toasts, { id, message }];
		setTimeout(() => {
			this.toasts = this.toasts.filter((t) => t.id !== id);
		}, durationMs);
	}
}
