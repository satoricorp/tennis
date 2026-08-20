# Tennis marketing site — build plan

*One page. Keep it **simple**: no analytics, no trackers, no second page to
keep in sync. Verify every claim against `../README.md` before shipping it.*

## Goal

Convince a developer, in one screen, that Tennis finds things their grep
can't — and that trying it costs one command and no API key.

Primary CTA: **install** (`go install github.com/satoricorp/tennis/cmd/tennis@latest`).
Secondary: **Releases** (a binary, for people without a Go toolchain) and
**GitHub** — https://github.com/satoricorp/tennis

## Tech

- Next.js app router, one route (`app/page.tsx` → `components/Hero.tsx`)
- Geist + Geist Mono from `next/font/google` for UI and the terminal blocks
- Two licensed faces self-hosted from `www/fonts`, both narrower than the MIT
  code they advertise — Apple Garamond Light Italic (wordmark) and Berkeley
  Mono (the pitch). Check the licence covers web embedding before shipping.
- No analytics / trackers

## Page

1. **Home** — wordmark, tagline, three terminal blocks (install, seed, match),
   two facts, links.

The third block is the argument: `"keep me signed in"` returns `auth.md`, a
document that shares no words with the query. Install instructions beyond the
one-liner live in the README, so there is one place to keep current when the
CLI changes.

## Brand

- Wordmark: Apple Garamond Light Italic, "Tennis" — the product is the proper
  noun; `tennis` stays lowercase everywhere it is the binary you type
- No accent colour: the page is ink on white, and the only colour is inside
  the terminal block (the quoted query, and the `[sem#1]` ranker tag)
- Tagline: "Store all of your sessions, take them anywhere." — set, with the
  sentence under it, in Berkeley Mono: the pitch reads as machine output

## Ship

```bash
cd www && npm install && npm run build
```
