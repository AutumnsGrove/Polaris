# Recommendation tools: judge first, then show

**Status: implemented 2026-10-06; unit-tested, not yet live-verified against a real deployment
(needs TMDB/Last.fm/Hardcover keys).**

## The problem

`books`, `movies` and `music` each fetch up to ~10 recommendations, look up a cover for every one
(Open Library/Hardcover cover, TMDB poster, Deezer album art), and then `ctx.AddCard` all of them.
The frontend renders those as the end-of-turn carousel (`mediaCards` in `ChatTurnView.svelte`)
whether or not the model ever vouched for them. Two consequences seen live:

- The recommendation quality is uneven (the books ranking especially), but the user sees every
  candidate regardless of whether the model would have recommended it.
- When the model does the sensible thing and calls `show` itself, the user gets the model's picks
  *plus* the full unfiltered carousel underneath.

`image_search` hit the same problem in issue #124 and was fixed by making display an explicit,
separate step (`docs/plans/show.md`, "Extended 2026-09-29"). This applies the same treatment.

## The change

1. **Nothing is auto-displayed.** The three tools stop calling `AddCard`. Each result goes into the
   existing per-turn candidate pool (`Context.ImageCandidates`, the same 1-based numbering
   `view_image`/`show`/`highlight` already resolve against, persisted per thread by
   `gateway/image_candidates.go`) via `AddRecommendationCandidate`.
2. **The text result is numbered by pool number**, with title, author/year/artist, the
   description or "why it fits" line, and a footer telling the model the user has *not* seen any of
   it: pick with `highlight` (`image_index` + a per-item `why`, ≤5, link-out cards — the best fit
   for recommendations) or `show` (`image_indices`, a cover gallery).
3. **Covers get bigger.** Candidates carry a `FullImageURL` where the source offers one (Open
   Library `-L`, TMDB `w780`, Deezer `cover_big`) so `show`'s gallery doesn't upscale a thumbnail.
4. **A candidate need not have a cover.** The pool previously treated "no image" as a gap marker.
   A gap is now "no image *and* no URL", so a coverless book can still be highlighted. `show`
   rejects a coverless pick with a message pointing at `highlight`; `view_image` already errored.
5. **Dedup is by page URL for these candidates, not by image URL.** Deezer returns the *album's*
   cover for a track, so several recommended tracks share one image; image-URL dedup would have
   collapsed them onto one number.
6. Tool descriptions, the `book`/`film_tv`/`music` prompt injections, `show`/`highlight`
   descriptions and `docs/FEATURES.md` say the numbers come from these tools too.

## Deliberately not changed

- Ranking (Hardcover/Open Library blend, TMDB recs, Last.fm similarity). Gating display lets the
  model drop a weak list; improving the ranking is separate work.
- The frontend carousel (`RecommendationsCarousel`, `mediaCards`). Nothing produces `kind: ""`
  cards anymore, but persisted messages from before this change still carry them and must keep
  rendering.
- No `attach_gallery`-style opt-out. Add one only if "just list them" requests turn out worse.
