// Every tunable for the start screen's night sky lives in this one object, so
// "make it calmer / busier / bigger" is an edit here and nowhere else. Times
// are in seconds, distances in CSS px unless a name says "Frac" (a fraction
// of the relevant viewport dimension). Mockup: mockups/polaris-living-sky.html.
export const SKY = {
	// Whole-canvas frame-rate cap. The sky is slow ambient motion, so 30fps is
	// visually identical to 60 and halves the battery cost on a phone.
	maxFps: 30,

	// Star counts per parallax layer (far to near). Each layer drifts sideways
	// at its own speed (fraction of screen width per second) and shifts with
	// the pointer by its own amount, which is what reads as depth. Twinkle
	// speed is per-star and independent of the layer.
	layers: [
		{ count: 60, maxRadius: 0.6, drift: 0.004, parallaxPx: 6, brightness: 0.5, seed: 31 },
		{ count: 40, maxRadius: 1.1, drift: 0.009, parallaxPx: 14, brightness: 0.75, seed: 32 },
		{ count: 18, maxRadius: 2, drift: 0.016, parallaxPx: 28, brightness: 1, seed: 33 }
	],
	// Fraction of stars tinted the warm accent instead of the cool one.
	warmStarChance: 0.16,

	// The Milky Way band: a diagonal soft wash plus a scatter of dust specks.
	milkyWay: { tilt: -0.5, halfWidthFrac: 0.16, washAlpha: 0.09, dustCount: 220, dustAlpha: 0.3 },

	comet: {
		firstDelay: 7,
		// Gap between comets, uniformly random within this range.
		gapMin: 25,
		gapMax: 40,
		duration: 2.6,
		tailFrac: 0.36 // tail length as a fraction of min(width, height)
	},

	constellations: {
		// At most this many on screen at once.
		maxConcurrent: 3,
		// Seconds until the first one appears after the screen loads, then a
		// random gap between one new arrival and the next. Raising these is the
		// main "calmer" dial.
		firstDelay: 1.5,
		spawnGapMin: 7,
		spawnGapMax: 14,
		// Seconds a fully drawn constellation stays on screen. Drawing in and
		// retracting are each (edges * secondsPerEdge + a small tail) long.
		hold: 8,
		secondsPerEdge: 0.5,
		// Longest side of a constellation, as a fraction of min(width, height*0.6),
		// clamped to the px range, then jittered by a random factor in sizeJitter.
		sizeFrac: 0.3,
		minSizePx: 90,
		maxSizePx: 240,
		sizeJitter: [0.8, 1.1],
		// Random tilt applied on top of the random mirror, in degrees (+/-).
		maxTiltDeg: 30,
		labelOpacity: 0.5,
		labelHeightPx: 16,
		labelCharPx: 5.8
	},

	placement: {
		// Constellations never overlap these, whichever of them is placed.
		edgeMarginPx: 14,
		avoidPadPx: 18, // clear space kept around the heading/composer
		gapPx: 26, // clear space kept between two constellations
		// Best-candidate sampling: draw up to `tries` random positions, keep up
		// to `candidates` valid ones, then use whichever is farthest from
		// everything on screen and from the last `recentMemory` spots used.
		// That last part is what stops the same few homes repeating.
		tries: 60,
		candidates: 8,
		recentMemory: 5,
		// Retry delay when no free spot exists (a tiny window, a full screen).
		retryDelay: 2
	},

	// What the sky avoids painting a constellation over, found inside the
	// canvas's parent element.
	avoidSelector: '.welcome-heading, .welcome .subtitle, .welcome-composer'
} as const;

export type SkyConfig = typeof SKY;
