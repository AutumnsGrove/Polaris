// categoryColors.ts maps Constellation's fixed category list to its own
// bespoke --color-cat-* token (see app.css) — one lookup shared by StarCard
// and anywhere else a star's category needs a color, same reasoning as
// categoryIcons.ts's single iconForCategory lookup.
//
// Each of the 32 fixed categories gets a genuinely distinct hue rather than
// sharing one across a thematic group — an earlier pass grouped categories
// into 7 hue families and distinguished members by lightening/darkening
// (color-mix toward white/black), but every darkened step read as muted or
// "disabled," which defeated the point. Plain distinct hues read as
// consistently vivid instead — see app.css's --color-cat-* block for why
// this isn't run through the categorical color validator (colorblind safety
// is a real, accepted tradeoff at 32 hues; icon + category-name label
// already carry identity everywhere this color shows up).
const CATEGORY_COLOR_VAR: Record<string, string> = {
	technology: '--color-cat-technology',
	'software engineering': '--color-cat-software-engineering',
	'ai & machine learning': '--color-cat-ai-machine-learning',
	science: '--color-cat-science',
	'space & astronomy': '--color-cat-space-astronomy',
	'nature & environment': '--color-cat-nature-environment',
	travel: '--color-cat-travel',
	'outdoor & fitness': '--color-cat-outdoor-fitness',
	history: '--color-cat-history',
	'politics & world affairs': '--color-cat-politics-world-affairs',
	'culture & society': '--color-cat-culture-society',
	'religion & spirituality': '--color-cat-religion-spirituality',
	'philosophy & ideas': '--color-cat-philosophy-ideas',
	'internet & social media': '--color-cat-internet-social-media',
	literature: '--color-cat-literature',
	'writing & language': '--color-cat-writing-language',
	music: '--color-cat-music',
	'film & tv': '--color-cat-film-tv',
	'video games': '--color-cat-video-games',
	'art & design': '--color-cat-art-design',
	'fashion & style': '--color-cat-fashion-style',
	'hobbies & crafts': '--color-cat-hobbies-crafts',
	'food & drink': '--color-cat-food-drink',
	'sports & recreation': '--color-cat-sports-recreation',
	'career & work': '--color-cat-career-work',
	'business & economy': '--color-cat-business-economy',
	'home & diy': '--color-cat-home-diy',
	automotive: '--color-cat-automotive',
	'finance & shopping': '--color-cat-finance-shopping',
	'education & learning': '--color-cat-education-learning',
	'health & wellness': '--color-cat-health-wellness',
	'relationships & family': '--color-cat-relationships-family'
};

// colorForCategory returns a ready-to-use CSS color value — callers assign
// it straight to --star-color, e.g.
// `style="--star-color: ${colorForCategory(star.category)}"`. Falls back to
// --color-personal — the same violet reserved for "outside the fixed list"
// that categoryIcons.ts's Sparkles fallback marks with an icon (and that the
// 32 hues above are deliberately spaced at least 15deg clear of).
export function colorForCategory(category: string): string {
	const token = CATEGORY_COLOR_VAR[category];
	return `var(${token ?? '--color-personal'})`;
}
