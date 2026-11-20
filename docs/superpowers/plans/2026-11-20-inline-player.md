# Inline Player Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the watch page into a normal page in the app's shell, with a 16:9 player box, info below it, and the season's episodes for an episode. The player cleans up when htmx swaps the page away.

**Architecture:** `watch.html` renders `.watch` > `#player` box + `.watch-info`. The `watch` handler adds the info and season episodes. `player.js` is refactored into `start(root)`/`teardown()`, with every global listener tracked, and runs on load, on `htmx:afterSwap` (new `#player`) and before an htmx swap that removes it.

**Tech Stack:** Go/gin/html/template, goquery tests, vanilla JS and CSS, htmx (theme v0.2.0).

**Spec:** `docs/superpowers/specs/2026-11-20-inline-player-design.md`

## Global Constraints

- No ← back bar and no top bar inside the player. The title lives in `.watch-info`.
- Player box: `aspect-ratio: 16/9; width: 100%; max-height: 75vh`. Edge to edge at ≤640px. About 1200px max and centred on desktop.
- Fullscreen still works through `requestFullscreen` on `#player`. `:fullscreen` (and `:-webkit-full-screen`) makes it fill the screen.
- `#player` has `hx-history="false"`.
- No theme changes, no rotation behaviour, no in-place episode switching.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- Leaving the watch page through a header/bottom-nav htmx swap sends `/progress` with `event=stop` exactly once and leaves no keydown handler behind: pressing Space on the next page doesn't throw or toggle anything (Task 2 harness check).
- Reaching the watch page through htmx (e.g. browser back after an htmx nav) starts exactly one player: one `/play` request and one keydown listener (Task 2 harness check).
- An episode with no `ParentIndex` (0) or a series lookup failure still renders the page, just without the list (Task 1 test).
- The seek bar, the ⚙ menu position and the centre play cluster still work inside the box at 360px, as in the earlier player checks (Task 2 harness).

---

### Task 1: Page layout and data

**Files:**
- Modify: `internal/handlers/player.go` (`watchView`, `watch`)
- Modify: `internal/templates/pages/watch.html`, `internal/static/css/media.css` (player block), i18n (`watch.episode` = "S%d · E%d")
- Test: `internal/handlers/player_test.go`

**Interfaces:**
- Produces: `watchView` gains `Kind string` ("movie"/"episode"), `Year int`, `Overview, ShowTitle, EpisodeName string`, `Season, Episode int`, `Episodes []episodeView` (reusing the show page's `episodeView`), `CurrentID string`. The `.watch-info` markup uses `h1` = title (movie) or show title (episode), `.watch-sub`, `p.overview`, and `ol.episodes` with `li.episode[.playing]` rows linking `/watch/<id>`.

- [ ] **Step 1: Failing tests.**
  - `TestWatchPageInline`: for a movie, no `.player-top` and no `[data-action=back]`. `.watch-info h1` = "Dune (2021)", and `#player[hx-history=false]` exists.
  - `TestWatchEpisodeList`: an episode e2 of a series with seasons 1 (e1, e2, e3) and 2 (e4). The `.watch-info .episodes .episode` count is 3, the second has class `playing`, the first links to `/watch/<e1>`, a watched e1 has class `watched`, and `.watch-sub` contains "S1 · E2".
  - `TestWatchEpisodeNoSeason`: `ParentIndex` 0 → 200, with no `.episodes`.
- [ ] **Step 2:** `make test` → FAIL.
- [ ] **Step 3: Implement.**
  - Handler: for an episode, get `Catalog.Episodes(SeriesID)`, keep `ParentIndex == it.ParentIndex` (only when > 0), mark watched with `Watching.Many`, and build `episodeView`s the same way `show` does. Errors only log.
  - Template: remove `.player-top`. Wrap the page in `<div class="watch">`. The info section reuses the show page's episode row markup.
  - CSS: the box rules from the constraints. The `.player` controls stay absolutely positioned inside it. Remove `.player-top` rules. Add a `.watch-info` gutter and `.episode.playing` (accent left border).
- [ ] **Step 4:** `make test`, `make check` → PASS.
- [ ] **Step 5: Commit** `feat(web): watch page in the app's shell with info and the season's episodes`

### Task 2: Player start and clean-up with htmx

**Files:**
- Modify: `internal/static/js/player.js`
- Harness (not committed): scratchpad `harness/` from the earlier player work. Re-dump the watch page, then add `htmx.min.js` and a fake nav button `hx-get="/other.html" hx-target="#content" hx-push-url="true"`, where `other.html` is a plain page.

**Interfaces:**
- Produces, inside `player.js`:
  - `function start(root)`: everything the IIFE did, scoped to `root`. Returns `teardown`.
  - `function on(target, type, fn, opts)`: registers a listener and records it for teardown.
  - The module level then does three things:
    - start a `#player` on load;
    - on `htmx:beforeSwap`, if `evt.detail.target.contains(current root)`, call `teardown()`;
    - on `htmx:afterSwap`, start a new `#player` that has no `data-ready`.
- `teardown()`:
  - if not leaving yet, set `leaving = true` and `save("stop")`;
  - destroy hls, clear the idle, tap and flash timers, and remove the recorded listeners;
  - pause the video, then `removeAttribute("src")` and `load()` to free the stream.

- [ ] **Step 1: Write the harness checks** (in `checks4.js`, same style as before). Each must fail on the current code:
  - `L1_stopOnNav`: start playing, click the nav button, wait → `H.calls` has exactly one `/progress` call with `event=stop`, and after the swap a dispatched Space keydown changes nothing and logs no console errors.
  - `L2_singleStartOnHtmxInsert`: htmx-swap the watch page fragment into `#content` twice (away and back) → each insertion makes exactly one `/play` call, and a Space keydown toggles play exactly once.
  - Also re-run the earlier `runChecks`, `runChecks2` and `runChecks3`, adjusted for the box: no `.player-top`, and the iframe sizes stay. All must still pass.
- [ ] **Step 2: Run the checks** → L1 and L2 FAIL.
- [ ] **Step 3: Implement** the refactor. Keep behaviour identical apart from lifecycle.
- [ ] **Step 4: Run the checks** → all PASS. Then `make test` and `make check`.
- [ ] **Step 5: Visual check** at 360px and desktop: a movie and an episode page, the player in fullscreen via the ⛶ button (the icon swap can be simulated), and the ⚙ menu open inside the box.
- [ ] **Step 6: Commit** `feat(web): player starts and cleans up around htmx page swaps`
