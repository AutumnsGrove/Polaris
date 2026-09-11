// categoryIcons.ts maps Constellation's fixed category list (see
// prompts.yaml's weaver.system) to a themed lucide icon — one lookup shared
// by every place that renders a star's category (StarCard, the star detail
// page, the Inbox review page, the Library's search results), replacing
// what used to be three separate ad hoc iconFor() functions that had each
// drifted out of sync with the actual category list over time.
import {
	Cpu,
	Terminal,
	BrainCircuit,
	FlaskConical,
	Telescope,
	Leaf,
	Landmark,
	Globe,
	BookOpen,
	PenLine,
	Music,
	Clapperboard,
	Gamepad2,
	Palette,
	Puzzle,
	UtensilsCrossed,
	Plane,
	Trophy,
	Mountain,
	HeartPulse,
	Briefcase,
	TrendingUp,
	Lightbulb,
	Church,
	Users,
	Share2,
	HandHeart,
	Hammer,
	Car,
	Shirt,
	Wallet,
	GraduationCap,
	Sparkles
} from '@lucide/svelte';

export const CATEGORY_ICONS: Record<string, typeof Cpu> = {
	technology: Cpu,
	'software engineering': Terminal,
	'ai & machine learning': BrainCircuit,
	science: FlaskConical,
	'space & astronomy': Telescope,
	'nature & environment': Leaf,
	history: Landmark,
	'politics & world affairs': Globe,
	literature: BookOpen,
	'writing & language': PenLine,
	music: Music,
	'film & tv': Clapperboard,
	'video games': Gamepad2,
	'art & design': Palette,
	'hobbies & crafts': Puzzle,
	'food & drink': UtensilsCrossed,
	travel: Plane,
	'sports & recreation': Trophy,
	'outdoor & fitness': Mountain,
	'health & wellness': HeartPulse,
	'career & work': Briefcase,
	'business & economy': TrendingUp,
	'philosophy & ideas': Lightbulb,
	'religion & spirituality': Church,
	'culture & society': Users,
	'internet & social media': Share2,
	'relationships & family': HandHeart,
	'home & diy': Hammer,
	automotive: Car,
	'fashion & style': Shirt,
	'finance & shopping': Wallet,
	'education & learning': GraduationCap
};

// Sparkles, not BookOpen — a category outside the fixed list (Weaver's rare
// escape hatch, see weaver.system's category instructions) shouldn't get a
// misleadingly specific icon implying it's literature/reading-related.
export function iconForCategory(category: string): typeof Cpu {
	return CATEGORY_ICONS[category] ?? Sparkles;
}
