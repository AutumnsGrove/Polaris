# README screenshots

Living checklist. Goal: show what Polaris *looks like* in the README and a gallery page
([`docs/SHOWCASE.md`](../SHOWCASE.md), images in `docs/screenshots/`), and shrink the README's
tool-by-tool prose.

## Capture setup (how these were made)

- Isolated sample instance: its own config, its own database, its own `code_exec` sandbox dirs, **no R2
  backups** (so nothing touches real data or the real bucket), on a port that isn't the dev stack's.
  Model `mimo` (MiMo v2.6 Flash, the cheapest); generic persona "Jane Doe" in Denver; all queries generic.
- Content is real model output, not fakeopenrouter: these images are the pitch, so canned answers would
  look canned. Whole set cost well under a dollar (Daily run: ~$0.027).
- Phone viewport 430×932 @2x is the default; desktop 1440×900 @2x for the core chat, Daily, Constellation,
  Atlas. Dark is primary; the main chat is also shot in light and composited into a diagonal split.
- Stored as WebP (Chromium canvas encoder, q0.9, desktop downscaled to 1800px wide) — 27 MB of PNG became
  ~3.4 MB. GIFs are kept as GIFs. Re-shoots add to git history, so keep re-shoots deliberate.

## Shots

Status: `[x]` captured + published, `[-]` dropped.

- [x] Start screen (night sky, constellation, comet) — phone + desktop
- [x] Answer with citations + tool trace (hero) — phone + desktop, dark + light, **diagonal split composite**
- [x] Recommendation carousel (books)
- [x] `code_exec`: inline themed chart + CSV document card
- [x] Weather range card + personalized verdict (inside the Pulsar report)
- [x] Atlas (`/search`) — phone + desktop
- [x] Memory panel with 4 memories — phone + desktop
- [x] Fields: list + one Field detail — phone + desktop
- [x] Pulsar: routine list (cropped) + a fired routine's report
- [x] The Daily: desktop three-column, plus a tall variant
- [x] Constellation library (9 stars from 5 "Jane Doe" threads) — phone + desktop
- [x] Oracle mode: GIF of the "reading" constellation (phone + desktop) + Settings panel
- [x] Transponder idle state — phone
- [x] Settings + Help (`?`) glossary — phone + desktop
- [-] Branching switcher, ghost threads, expanded tool trace (trace is visible in the hero)
- [-] Nearby-places cards (the real run came back text-only, and SearXNG's image engines were
  rate-limited mid-session, so the answer said "search is down" — not showcase material)
- [-] Constellation star map (9 unlabeled dots reads as empty; the Library view is the strong shot)
- [-] Oracle "Read as medical" reply still (dosing advice isn't something to feature in a README; the GIF
  and the Settings panel carry Oracle instead)

## Notes for future re-shoots

- The Daily's `daily-desktop-tall` variant contains real news headlines (including political ones); the
  default `daily-desktop` crop deliberately doesn't. Real news dates the image fast.
- `picture_of_day` failed in the sample run (SearXNG image engines rate-limited), so that block is absent.
- Oracle GIF frame count matters: a long single `duration` on the last concat entry gets applied twice by
  ffmpeg's concat demuxer — hold a frame by repeating it instead.

## Final steps

- [x] Screenshots published to `docs/screenshots/`
- [ ] `docs/SHOWCASE.md` gallery
- [ ] Trim README features list, add hero images + link to gallery (long list moves to `docs/FEATURES.md`)
- [ ] `DEVELOPMENT.md`: how to re-shoot
- [ ] Commit per stage
