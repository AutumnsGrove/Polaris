<script lang="ts">
	// A persisted read-aloud clip's player — the waveform-as-scrubber design
	// chosen in docs/plans/voice-playback-infra.md (Option D in
	// mockups/transponder-audio-player-options.html, combining Option B's
	// waveform look with Option C's bordered-card/real-scrubbing convention).
	// Bars are a real decoded waveform (peak amplitude per bucket via
	// AudioContext.decodeAudioData), not a decorative placeholder — the
	// underlying file is small (a few seconds of speech) so decoding it
	// client-side once, on mount, is cheap. Falls back to a flat placeholder
	// pattern if decoding fails for any reason (unsupported browser, network
	// hiccup) — playback/scrubbing still works either way, since bar heights
	// are purely cosmetic and never drive seek math.
	import { Volume2, Download } from '@lucide/svelte';

	// autoplay: true only for the turn whose synthesis just finished in
	// this same click (see AudioPlayer.justFinishedIndex's doc comment) —
	// never true for a player mounted from reopening a thread. Consumed
	// once, on mount, via the effect below; this component only ever
	// mounts once per turn (turn.ttsAudioFile goes from unset to set
	// exactly once), so there's no risk of replaying on a later rerender.
	let { src, autoplay = false }: { src: string; autoplay?: boolean } = $props();

	const BAR_COUNT = 40;

	let audioEl: HTMLAudioElement | undefined = $state();
	let waveformEl: HTMLDivElement | undefined = $state();
	let duration = $state(0);
	let currentTime = $state(0);
	let isPlaying = $state(false);
	let peaks = $state<number[]>(Array(BAR_COUNT).fill(0.4));
	let dragging = $state(false);
	let autoplayed = false;

	$effect(() => {
		if (autoplay && audioEl && !autoplayed) {
			autoplayed = true;
			void audioEl.play();
		}
	});

	const progress = $derived(duration > 0 ? currentTime / duration : 0);

	function formatTime(seconds: number): string {
		if (!Number.isFinite(seconds) || seconds < 0) return '0:00';
		const m = Math.floor(seconds / 60);
		const s = Math.floor(seconds % 60);
		return `${m}:${s.toString().padStart(2, '0')}`;
	}

	async function decodePeaks() {
		try {
			const res = await fetch(src);
			const arrayBuffer = await res.arrayBuffer();
			const ctx = new (window.AudioContext || (window as any).webkitAudioContext)();
			const buffer = await ctx.decodeAudioData(arrayBuffer);
			const channel = buffer.getChannelData(0);
			const bucketSize = Math.floor(channel.length / BAR_COUNT) || 1;
			const raw: number[] = [];
			let maxPeak = 0;
			for (let i = 0; i < BAR_COUNT; i++) {
				let peak = 0;
				const start = i * bucketSize;
				const end = Math.min(start + bucketSize, channel.length);
				for (let j = start; j < end; j++) {
					const abs = Math.abs(channel[j]);
					if (abs > peak) peak = abs;
				}
				raw.push(peak);
				if (peak > maxPeak) maxPeak = peak;
			}
			// Synthesized speech has a much narrower dynamic range than music
			// (Kokoro's output peaks cluster fairly tightly), which made the
			// waveform look flatter/less dramatic than the mockup's — so
			// stretch this clip's own peaks to fill 0..1 first (normalize
			// against its actual max, not a fixed constant) and then apply a
			// >1 exponent, which pushes quieter buckets down further than
			// loud ones and exaggerates the visual contrast between them.
			const next = raw.map((peak) => {
				const normalized = maxPeak > 0 ? peak / maxPeak : 0;
				const exaggerated = Math.pow(normalized, 1.8);
				// Floor at 0.12 so a near-silent bucket (a pause between
				// sentences) still renders a visible sliver, matching the
				// mockup's waveform look rather than gapping out entirely.
				return Math.max(0.12, exaggerated);
			});
			peaks = next;
			void ctx.close();
		} catch (err) {
			// Flat placeholder pattern (already the initial value) is fine —
			// decoding is cosmetic only, never required for playback/seeking.
			console.error('waveform decode failed', err);
		}
	}

	$effect(() => {
		void decodePeaks();
	});

	function togglePlay() {
		if (!audioEl) return;
		if (isPlaying) {
			audioEl.pause();
		} else {
			void audioEl.play();
		}
	}

	function seekFromClientX(clientX: number) {
		if (!waveformEl || !audioEl || duration <= 0) return;
		const rect = waveformEl.getBoundingClientRect();
		const fraction = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
		currentTime = fraction * duration;
		audioEl.currentTime = currentTime;
	}

	function onPointerDown(e: PointerEvent) {
		dragging = true;
		(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
		seekFromClientX(e.clientX);
	}
	function onPointerMove(e: PointerEvent) {
		if (!dragging) return;
		seekFromClientX(e.clientX);
	}
	function onPointerUp(e: PointerEvent) {
		dragging = false;
		(e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
	}
</script>

<div class="tts-card">
	<div class="tts-card-header">
		<div class="tts-card-title">
			<Volume2 size={14} color="var(--color-accent-2)" />
			<span>Audio · {formatTime(duration)}</span>
		</div>
		<a class="icon-btn" href={src} download title="Download audio">
			<Download size={14} />
		</a>
	</div>
	<div class="tts-card-body">
		<button class="play-btn" onclick={togglePlay} title={isPlaying ? 'Pause' : 'Play'}>
			{#if isPlaying}
				<svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"
					><rect x="6" y="4" width="4" height="16" /><rect x="14" y="4" width="4" height="16" /></svg
				>
			{:else}
				<svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z" /></svg>
			{/if}
		</button>
		<div
			class="waveform"
			bind:this={waveformEl}
			onpointerdown={onPointerDown}
			onpointermove={onPointerMove}
			onpointerup={onPointerUp}
			role="slider"
			aria-label="Seek"
			aria-valuemin={0}
			aria-valuemax={duration}
			aria-valuenow={currentTime}
			tabindex="0"
		>
			<div class="waveform-bars">
				{#each peaks as peak, i (i)}
					<div
						class="wf-bar"
						class:played={i / BAR_COUNT <= progress}
						style="height: {Math.round(peak * 28)}px"
					></div>
				{/each}
			</div>
			<div class="playhead-hit" style="left: {progress * 100}%">
				<div class="playhead-line"></div>
				<div class="playhead-grip"></div>
			</div>
		</div>
		<span class="time-readout">{formatTime(currentTime)} / {formatTime(duration)}</span>
	</div>
</div>

<audio
	bind:this={audioEl}
	{src}
	preload="metadata"
	onloadedmetadata={() => (duration = audioEl?.duration ?? 0)}
	ontimeupdate={() => {
		if (!dragging) currentTime = audioEl?.currentTime ?? 0;
	}}
	onplay={() => (isPlaying = true)}
	onpause={() => (isPlaying = false)}
	onended={() => (isPlaying = false)}
	style="display: none"
></audio>

<style>
	.tts-card {
		max-width: 420px;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		padding: var(--space-md) var(--space-lg);
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
		margin-top: var(--space-sm);
	}

	.tts-card-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.tts-card-title {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-dim);
		letter-spacing: 0.02em;
	}

	.tts-card-header .icon-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 26px;
		height: 26px;
		border-radius: var(--radius-md);
		color: var(--color-text-dim);
	}
	.tts-card-header .icon-btn:hover {
		color: var(--color-text);
		background: var(--color-surface-3);
	}

	.tts-card-body {
		display: flex;
		align-items: center;
		gap: var(--space-md);
	}

	.play-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 36px;
		height: 36px;
		border-radius: var(--radius-full);
		background: var(--color-accent);
		border: none;
		color: var(--color-bg, #15110c);
		flex-shrink: 0;
	}

	.waveform {
		position: relative;
		flex: 1;
		height: 32px;
		display: flex;
		align-items: center;
		cursor: pointer;
		touch-action: none;
	}

	.waveform-bars {
		position: absolute;
		inset: 0;
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.wf-bar {
		width: 3px;
		flex-shrink: 0;
		border-radius: 1.5px;
		background: var(--color-border-strong);
	}
	.wf-bar.played {
		background: var(--color-accent);
	}

	/* 44px invisible touch target per the mockup's mobile-dragging note
	   (PRODUCT.md's accessibility minimum) even though the visible grip
	   stays small — a real fingertip is much wider than a 3px waveform bar. */
	.playhead-hit {
		position: absolute;
		top: 50%;
		width: 44px;
		height: 44px;
		transform: translate(-50%, -50%);
		display: flex;
		align-items: center;
		justify-content: center;
		pointer-events: none;
	}

	.playhead-line {
		position: absolute;
		width: 3px;
		height: 32px;
		background: var(--color-text);
		border-radius: var(--radius-full);
	}

	.playhead-grip {
		position: absolute;
		width: 14px;
		height: 14px;
		border-radius: var(--radius-full);
		background: var(--color-text);
		box-shadow:
			0 0 0 3px var(--color-surface-2),
			0 1px 4px oklch(0% 0 0 / 40%);
	}

	.time-readout {
		font-size: 11px;
		color: var(--color-text-dim);
		font-variant-numeric: tabular-nums;
		flex-shrink: 0;
	}
</style>
