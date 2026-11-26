# Subtitle Size Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `− 100% +` subtitle size control in the player's ⚙ menu, saved per person.

**Architecture:** One more column in `preferences`, carried through `db.Prefs` → `watching.Prefs` → `/prefs` and the watch page's `data-sub-size`. The player applies it with one injected `::cue` rule.

**Tech Stack:** Go 1.23, gin, Postgres (goose), html/template, plain JS.

**Spec:** `docs/superpowers/specs/2026-11-26-subtitle-size-design.md`

## Global Constraints

- Size is a percent: 50 to 250, steps of 10, default 100 (= 1.4em).
- `/prefs` rejects other values with 400.
- Labels in en and bg: `player.subSize`, `player.smaller`, `player.larger`.

## Review Focus

- A person with no `preferences` row: watch page shows 100%, not 0%. Test in Task 1.
- `/prefs` with only `subtitle_size`: languages kept. Test in Task 1 (db round trip).
- `subtitle_size=55`, `=40`, `=abc`: 400. Test in Task 1.
- Picture subtitle chosen: size row hidden (manual check, Task 2).
- TV remote: arrows reach − and +, OK presses them (manual check, Task 2).

---

### Task 1: Store and serve the size

**Files:**
- Create: `internal/db/migrations/00008_subtitle_size.sql`
- Modify: `internal/db/progress.go`, `internal/watching/service.go`, `internal/handlers/deps.go`, `internal/handlers/player.go`, `internal/templates/pages/watch.html`, `internal/handlers/fakes_test.go`
- Test: `internal/watching/service_test.go`, `internal/handlers/player_test.go`

**Interfaces:**
- Produces: `Prefs{SubtitleLang, AudioLang string; SubtitleSize int}` (db and watching); `SetPrefs(ctx, userID string, subtitleLang, audioLang *string, subtitleSize *int) error`; `watchView.SubSize int`; `data-sub-size` on `#player`.

- [ ] **Step 1: Tests.**
  - watching prefs round trip: `SetPrefs(nil, nil, &130)` after languages set → `Prefs{"off","bul",130}`; fresh user → `SubtitleSize` 0.
  - `TestPrefsSubtitleSize`: POST `/prefs` `subtitle_size=120` → 204, fake got 120; `55`, `40`, `260`, `abc` → 400.
  - `TestWatchSubSize`: fake prefs size 0 → `data-sub-size="100"`; 150 → `"150"`.
- [ ] **Step 2:** they fail (compile).
- [ ] **Step 3: Implement.** Migration `ALTER TABLE preferences ADD COLUMN subtitle_size INTEGER NOT NULL DEFAULT 100` (down drops it). `SetPrefs` upsert with `COALESCE($4, 100)` / `COALESCE($4, preferences.subtitle_size)`. Watch page reads `Watching.Prefs` (error logged, falls back to 100).
- [ ] **Step 4:** `make check && make test` pass.
- [ ] **Step 5:** commit `feat(watching): remember a subtitle size per person`.

### Task 2: The control in the player

**Files:**
- Modify: `internal/templates/pages/watch.html` (menu section `data-group="size"`), `internal/static/js/player.js`, `internal/static/css/media.css`, `internal/i18n/en.json`, `internal/i18n/bg.json`
- Test: `internal/handlers/player_test.go`

**Interfaces:**
- Consumes: `data-sub-size`, `/prefs` `subtitle_size`.

- [ ] **Step 1: Test** `TestWatchSizeControl`: watch page has `data-group="size"` with buttons `data-size="-10"` and `data-size="10"` and aria-labels translated (not the key).
- [ ] **Step 2:** it fails.
- [ ] **Step 3: Implement.** Section in the menu: heading "Subtitle size", a row `<button data-size="-10">−</button><output>100%</output><button data-size="10">+</button>`. JS: `applySize(n)` writes `#player video::cue{font-size:calc(1.4em * n / 100)}` into one `<style>`, sets the output text, disables a button at 50/250; click saves `subtitle_size`. Hide the section when no subtitle or a picture subtitle is shown (in `fillTracks`). Remove the fixed `font-size` from the CSS `::cue` rule.
- [ ] **Step 4:** `make check && make test` pass; check in a browser: − and + resize a showing subtitle at once, the size survives a reload.
- [ ] **Step 5:** commit `feat(web): subtitle size control in the player`.
