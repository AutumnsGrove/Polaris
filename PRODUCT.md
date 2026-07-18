# Product

## Register

product

## Users

A single self-hosted user (the operator) accessing Polaris from a phone (primary) or laptop, over
their own Tailscale network. No public signup, no multi-tenant concerns. Sessions are typically
short, purposeful research queries — "what's the current stable version of Go", "find a coffee
shop near the Space Needle" — asked while doing something else (on the move, mid-task), not long
idle browsing.

## Product Purpose

Polaris is a self-hosted, search-augmented AI assistant: an agent loop (think / web_search /
web_read / nearby_search) backed by the user's own SearxNG instance instead of a paid search API,
running on a Le Potato SBC alongside the rest of their homelab. It answers questions by actually
looking things up and citing sources, closer to Kagi Assistant or Perplexity than a chatbot.
Success looks like: fast to trust an answer (clear sourcing), fast to act on it (retry/edit,
read-aloud, nearby-places), and invisible as software — the tool gets out of the way of the
research.

## Brand Personality

Quiet, editorial, confident — the feel of a considered research tool, not a dashboard or a chat
toy. Named Polaris: the fixed point you navigate by when you don't know the answer yourself
(external sources triangulated against, not a source of truth in itself). Icon is an astrophotograph
of the Beehive Cluster (M44) — the visual world is night sky, stars, quiet dark, not tech-neon.
Should feel calm and low-glare, closer to reading something well-typeset than operating a control
panel. Warmth is allowed (this is a personal tool, not enterprise software) but restraint always
wins over decoration.

## Anti-references

- **Generic SaaS / ChatGPT-Gemini clone look** — rounded message bubbles, purple-blue gradients,
  the default "AI product" template. Polaris should not read as "another chat app."
- **Neon/gamer dark mode** — high-saturation cyan/magenta glows, RGB accents, anything that reads
  as "hacker tool" rather than "research tool."
- Corporate SaaS polish in general (marketing-site sheen, hero-metric cards, badge-everything) is
  wrong for a single-user tool with no one else to impress.

## Design Principles

- **Sourcing is the product.** Citations, tool-call traces, and cost are the substance of the UI —
  design should make them legible and trustworthy, not decorative or buried.
- **Calm over clever.** No motion, color, or chrome that exists to look impressive rather than to
  clarify. If it doesn't help the user read or act faster, cut it.
- **Reading-first typography.** Answers are often long-form prose with citations; the UI is closer
  to a well-set reading surface than a dashboard. Line length, weight contrast, and spacing matter
  more than iconography.
- **Night-sky, not tech-neon.** When color or imagery is needed, draw from the Polaris/astro
  identity (deep neutrals, restrained warm or cool accent) rather than generic SaaS blue/purple.
- **Mobile is the primary surface.** Every design decision should be checked against a one-handed
  phone session first, desktop second.

## Accessibility & Inclusion

No formal WCAG target stated, but maintain solid contrast in both themes (already has dark/light
via `data-theme`), keep tap targets comfortable for one-handed mobile use, and respect
`prefers-reduced-motion` for any new animation work — this is used one-handed, often on the move.
