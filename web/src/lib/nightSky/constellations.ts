// The constellation pool. To add one, append an entry: `points` are any
// sketch coordinates (only their shape matters, normalize() rescales them) and
// `edges` are index pairs into `points`, drawn in the listed order. Edge order
// is animation order: each line draws from its first point to its second, and
// a star lights when the first line touching it begins. Shapes are simplified
// asterisms, drawn to be recognizable, not astronomically exact.
export interface ConstellationDef {
	name: string;
	points: [number, number][];
	edges: [number, number][];
}

export const CONSTELLATIONS: ConstellationDef[] = [
	{
		name: 'URSA MINOR',
		points: [[0, 0], [-0.06, 0.1], [-0.12, 0.2], [-0.17, 0.32], [-0.22, 0.3], [-0.27, 0.44], [-0.17, 0.46]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 4], [4, 5], [5, 6], [6, 3]]
	},
	{
		name: 'CASSIOPEIA',
		points: [[0, 0.04], [0.07, 0.13], [0.16, 0.06], [0.24, 0.17], [0.33, 0.08]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 4]]
	},
	{
		name: 'CEPHEUS',
		points: [[0, 0], [0.1, 0.05], [0.2, 0.16], [0.13, 0.24], [0.02, 0.21]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 4], [4, 0], [1, 4]]
	},
	{
		name: 'URSA MAJOR',
		points: [[0, 0.1], [0.1, 0.16], [0.2, 0.12], [0.16, 0.04], [0.28, 0.02], [0.38, 0.06], [0.46, 0.16]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 0], [3, 4], [4, 5], [5, 6]]
	},
	{
		name: 'ORION',
		points: [[0, 0], [0.16, 0.02], [0.04, 0.11], [0.08, 0.12], [0.12, 0.13], [0, 0.24], [0.15, 0.25]],
		edges: [[0, 1], [1, 4], [4, 3], [3, 2], [2, 0], [2, 5], [4, 6], [6, 5]]
	},
	{
		name: 'LEO',
		points: [[0, 0.16], [0.03, 0.09], [0.08, 0.04], [0.15, 0.02], [0.17, 0.09], [0.3, 0.07], [0.32, 0.15]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 4], [4, 0], [3, 5], [5, 6], [6, 4]]
	},
	{
		name: 'SCORPIUS',
		points: [[0, 0.04], [0.05, 0.06], [0.06, 0.14], [0.05, 0.22], [0.09, 0.29], [0.17, 0.32], [0.23, 0.27], [0.22, 0.21], [0.1, 0]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 4], [4, 5], [5, 6], [6, 7], [1, 8]]
	},
	{
		name: 'CYGNUS',
		points: [[0.1, 0], [0.1, 0.12], [0.1, 0.3], [0, 0.16], [0.22, 0.08]],
		edges: [[0, 1], [1, 2], [1, 3], [1, 4]]
	},
	// The entries below are traced from the IAU/Sky & Telescope charts (pixel
	// coordinates, sky orientation: east left, north up), so they're larger
	// numbers than the hand-sketched ones above; normalize() makes that moot.
	{
		name: 'GEMINI',
		points: [[162, 106], [139, 141], [212, 124], [252, 90], [190, 197], [262, 172], [300, 212], [270, 250], [186, 247], [262, 282]],
		edges: [[3, 2], [2, 0], [2, 5], [5, 6], [1, 4], [4, 5], [4, 7], [4, 8], [7, 9]]
	},
	{
		name: 'TAURUS',
		points: [[166, 102], [270, 200], [139, 168], [312, 222], [349, 247], [428, 265], [352, 306]],
		edges: [[0, 1], [1, 2], [1, 3], [3, 4], [4, 5], [5, 6]]
	},
	{
		name: 'LYRA',
		points: [[281, 169], [265, 183], [240, 188], [230, 237], [255, 230]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 4], [4, 1]]
	},
	{
		name: 'AQUILA',
		points: [[222, 177], [225, 156], [236, 141], [288, 208], [330, 112], [327, 278], [180, 243], [347, 102]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 4], [4, 7], [3, 5], [3, 6]]
	},
	{
		name: 'PEGASUS',
		points: [[120, 127], [208, 140], [200, 218], [99, 210], [247, 276], [286, 272], [324, 252], [237, 128], [281, 110], [228, 160], [318, 154]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 0], [2, 4], [4, 5], [5, 6], [1, 7], [7, 8], [1, 9], [9, 10]]
	},
	{
		name: 'SAGITTARIUS',
		points: [[262, 256], [288, 263], [318, 250], [322, 289], [318, 332], [245, 289], [357, 302]],
		edges: [[0, 1], [1, 2], [2, 3], [3, 4], [4, 5], [5, 0], [5, 1], [1, 3], [3, 6], [6, 4]]
	},
	{
		name: 'CANIS MAJOR',
		points: [[263, 162], [298, 185], [204, 261], [224, 284], [172, 289], [238, 176]],
		edges: [[1, 0], [0, 5], [5, 2], [2, 3], [2, 4], [4, 3]]
	},
	{
		name: 'ANDROMEDA',
		points: [[323, 311], [268, 298], [210, 254], [130, 185], [237, 230], [232, 205], [218, 150]],
		edges: [[0, 1], [1, 2], [2, 3], [2, 4], [4, 5], [5, 6]]
	}
];

// A constellation rescaled so its longest side is exactly 1, origin at its
// top-left corner. The size and tilt applied per showing come later.
export interface UnitShape {
	name: string;
	points: [number, number][];
	edges: [number, number][];
}

export function normalize(def: ConstellationDef): UnitShape {
	const xs = def.points.map((p) => p[0]);
	const ys = def.points.map((p) => p[1]);
	const x0 = Math.min(...xs);
	const y0 = Math.min(...ys);
	const span = Math.max(Math.max(...xs) - x0, Math.max(...ys) - y0) || 1;
	return {
		name: def.name,
		edges: def.edges,
		points: def.points.map(([x, y]) => [(x - x0) / span, (y - y0) / span])
	};
}
