<script lang="ts">
	import { Mic, Loader2 } from '@lucide/svelte';

	// The orb on each Transponder screen — always the mic: the same hold/tap
	// gesture wiring on every variant, so "the orb is the mic" holds on Idle/
	// Listening ('mic'), Thinking and Speaking alike. Content and styling per
	// variant live here so the parent only says which screen it is.
	//
	// Kept as a single <button> across Idle -> Listening (same component
	// instance, only `listening` flips): the same gesture that starts a
	// hold-mode recording is the one whose release ends it — swapping to a
	// different element mid-gesture breaks touch-event continuity, since
	// touchend can fail to fire at all once its original target has been
	// removed from the DOM.
	let {
		variant,
		listening = false,
		transcribing = false,
		paused = false,
		disabled = false,
		barLevels,
		scale,
		toggleMode,
		onDown,
		onUp,
		onToggle,
		label
	}: {
		variant: 'mic' | 'thinking' | 'speaking';
		listening?: boolean;
		transcribing?: boolean;
		paused?: boolean;
		disabled?: boolean;
		barLevels: number[];
		// Applied as transform: scale(); undefined leaves the orb un-scaled.
		scale?: number;
		toggleMode: boolean;
		onDown: () => void;
		onUp: () => void;
		onToggle: () => void;
		label: string;
	} = $props();
</script>

<button
	type="button"
	class="orb {variant}-orb"
	class:listening
	class:paused
	style:transform={scale !== undefined ? `scale(${scale})` : undefined}
	{disabled}
	onclick={toggleMode ? onToggle : undefined}
	onmousedown={toggleMode ? undefined : onDown}
	onmouseup={toggleMode ? undefined : onUp}
	onmouseleave={toggleMode ? undefined : onUp}
	ontouchstart={toggleMode
		? undefined
		: (e) => {
				e.preventDefault();
				onDown();
			}}
	ontouchend={toggleMode
		? undefined
		: (e) => {
				e.preventDefault();
				onUp();
			}}
	aria-label={label}
>
	{#if variant === 'mic'}
		{#if listening && transcribing}
			<Loader2 size={28} color="var(--color-accent)" class="spin" />
		{:else if listening}
			<div class="bars">
				{#each barLevels as level, i (i)}
					<div class="bar" style:height="{16 + level * 40}px"></div>
				{/each}
			</div>
		{:else}
			<Mic size={40} color="var(--color-accent)" />
		{/if}
	{:else if variant === 'thinking'}
		<div class="spinner"></div>
		<Loader2 size={26} color="var(--color-accent-2)" class="spin" />
	{:else}
		<!-- Real decoded-peaks data via barLevels, not a canned loop
		     — see Transponder.svelte's startPlaybackVisualizer doc comment. -->
		<div class="bars">
			{#each barLevels as level, i (i)}
				<div class="bar" style:height="{14 + level * 34}px"></div>
			{/each}
		</div>
	{/if}
</button>

<style>
	.orb {
		border-radius: var(--radius-full);
		display: flex;
		align-items: center;
		justify-content: center;
		background: radial-gradient(circle at 35% 30%, var(--color-surface-3), var(--color-surface) 70%);
		border: 1px solid var(--color-border-strong);
		/* Live-caught, confirmed via a documented Firefox WebRender bug
		   (bugzil.la/1662069, bugzil.la/731113): a border-radius circle
		   combined with transform: scale() at fractional scale factors can
		   mis-rasterize — visible as seam/line artifacts along the
		   circle's axes, worst exactly when the scale factor is changing
		   (which it constantly is here, every animation frame). will-change
		   hints the browser to promote this to its own stable GPU layer
		   and rasterize once, scaling the resulting bitmap smoothly,
		   instead of re-rasterizing the vector shape at a new fractional
		   factor every frame — the actual workaround that thread's
		   reporters found, not a guess. */
		will-change: transform;
		/* No transition on transform, deliberately — style:transform here
		   is already updated by JS on every animation frame (~60fps) while
		   audio is active. A CSS transition trying to interpolate toward a
		   target a 60fps loop keeps yanking away is a real, confirmed-live
		   source of visual tearing/snapping ("the element stretching too
		   far") — the orb's own pulse-ring box-shadow keyframe (removed)
		   was a second, independent animation compounding the same
		   problem, running on its own uncoordinated 1.8s schedule against
		   the real scale updates. JS driving the value every frame IS the
		   animation; a second system animating toward it just fights it. */
		/* Also fixes: a brief system "move/drag" cursor (renders as a
		   crosshair/plus in some browsers) flashed over the orb during a
		   press-and-hold — the orb's own bar children change height every
		   animation frame while held, and without this the browser can
		   read a mousedown-and-hold over fast-changing content as an
		   ambiguous drag/selection attempt and show its own drag-affordance
		   cursor instead of the plain pointer this button actually wants. */
		user-select: none;
		-webkit-user-select: none;
		-webkit-user-drag: none;
	}

	.bars,
	.bar {
		user-select: none;
		-webkit-user-select: none;
		-webkit-user-drag: none;
	}

	/* The push-to-talk control itself in Idle/Listening — a real <button>,
	   not a decorative div, so it can carry the same hold/tap handlers as
	   VoiceButton.svelte's .mic-btn (see this file's top doc comment on why
	   this stays one element across both phases). */
	.mic-orb {
		width: 168px;
		height: 168px;
		padding: 0;
		cursor: pointer;
		animation: breathe 3.6s var(--ease-out-expo) infinite;
	}

	.mic-orb:disabled {
		opacity: 0.5;
		cursor: default;
	}

	.mic-orb.listening {
		width: 148px;
		height: 148px;
		border-color: color-mix(in srgb, var(--color-accent) 45%, transparent);
		animation: none;
	}

	.thinking-orb {
		position: relative;
		width: 148px;
		height: 148px;
		padding: 0;
		cursor: pointer;
		border-color: var(--color-border-strong);
	}

	.speaking-orb {
		width: 128px;
		height: 128px;
		padding: 0;
		cursor: pointer;
		border-color: color-mix(in srgb, var(--color-accent) 45%, transparent);
	}

	/* !isPlaying: dim to signal "not producing anything right now" — a
	   moving orb over silence read as "still working" rather than what it
	   actually was, "stuck". */
	.speaking-orb.paused {
		opacity: 0.6;
	}

	.spinner {
		position: absolute;
		inset: 0;
		border-radius: var(--radius-full);
		border: 2px solid transparent;
		border-top-color: var(--color-accent-2);
		border-right-color: color-mix(in srgb, var(--color-accent-2) 25%, transparent);
		animation: spin 1.6s linear infinite;
	}

	.bars {
		display: flex;
		align-items: center;
		gap: 4px;
	}

	.bar {
		width: 4px;
		border-radius: 2px;
		background: var(--color-accent);
		transition: height 0.06s linear;
	}

	@keyframes breathe {
		0%, 100% { transform: scale(1); }
		50% { transform: scale(1.03); }
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	@media (prefers-reduced-motion: reduce) {
		.mic-orb,
		.speaking-orb {
			animation: none;
		}
	}
</style>
