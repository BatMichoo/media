# Manual Seeding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let anyone start seeding a finished torrent again for 24 h, 72 h or with no limit.

**Architecture:** Two new columns on `downloads` hold a chosen seeding end. `seedingOver` takes the row and uses that end (or no limit) instead of finish date + `SeedFor`; the existing poll then stops it. A new sheet on the Downloads page posts the choice.

**Tech Stack:** Go, Gin, HTMX, Postgres (goose migrations), testify.

**Spec:** docs/superpowers/specs/2026-11-24-manual-seeding-design.md

## Global Constraints

- The automatic 72 hours after finishing stays as is when nothing was chosen.
- Durations: exactly 24 h, 72 h, or no limit; counted from the click.
- EN and BG strings for everything shown.
- go.mod stays at Go 1.23 (no range-over-int in templates).

## Review Focus

- A chosen end in the past on a paused torrent: Resume must leave it stopped.
- Choosing a time while the torrent is paused clears Paused and starts it.
- An unfinished or unknown torrent posted to /seed directly: refused, nothing started or saved.
- A bad form value: 400, nothing changed.
- Removing and re-adding the same hash starts with no chosen end.

---

### Task 1: Store the chosen end

**Files:** Create `internal/db/migrations/00007_download_seed_until.sql`; modify `internal/db/downloads.go`; test `internal/db/db_test.go`.

**Interfaces:** Produces `db.Download.SeedUntil time.Time` (zero = none) and `db.Download.SeedNoLimit bool`, saved by `UpdateDownload`, read by `GetDownload`/`ListDownloads`.

- [ ] Test: update a row with `SeedUntil` set and `SeedNoLimit` true, read it back equal; a new row reads zero/false.
- [ ] Migration: `seed_until TIMESTAMPTZ NULL`, `seed_no_limit BOOLEAN NOT NULL DEFAULT false` (Down drops both).
- [ ] Scan with `sql.NullTime`; write NULL for zero time.
- [ ] Commit `feat(db): store a chosen seeding end`.

### Task 2: Seed in the service

**Files:** modify `internal/downloads/service.go`, `reconcile.go`, `download.go`; test `service_test.go`.

**Interfaces:** Produces `(*Service).Seed(ctx, hash string, limit time.Duration) error` (`limit` 0 = no limit); `Download.SeedingNoLimit bool`; `Download.CanSeed() bool` (= `SeedingDone && Live && !Paused`).

- [ ] Tests: `TestSeedForChosenTime` (24 h: started, SeedingUntil = now+24 h, poll at +23 h doesn't stop, at +25 h stops, then SeedingDone and CanSeed); `TestSeedNoLimit` (poll at +1000 h doesn't stop, SeedingNoLimit true); `TestSeedPauseResume` (pause, resume inside the time starts; past it doesn't); `TestSeedRefused` (unfinished → ErrInvalid, unknown hash → ErrNotFound, not started).
- [ ] `seedingOver(r db.Download, t transmission.Torrent)`: false when no DoneDate or `r.SeedNoLimit`; end = `r.SeedUntil` if set, else DoneDate+SeedFor. List and Poll and Resume pass the row.
- [ ] `Seed`: under mu; row; live torrent finished (`DoneDate` set) else ErrInvalid; Start; set SeedUntil/SeedNoLimit, Paused=false; save.
- [ ] Commit `feat(downloads): seed a finished torrent again`.

### Task 3: Button, sheet and route

**Files:** modify `internal/handlers/downloads.go`, `web.go`, `internal/templates/pages/downloads.html`, `internal/i18n/en.json`, `bg.json`; test `internal/handlers/downloads_test.go`.

**Interfaces:** Consumes `Seed`, `CanSeed`, `SeedingNoLimit`. Routes `GET /downloads/:hash/seed` (sheet `media/seed-sheet`), `POST /downloads/:hash/seed` with `for` = `24h` | `72h` | `none`.

- [ ] Tests: Seed button only on rows with CanSeed; sheet shows three radios (72 h checked); POST maps each value to 24 h/72 h/0 and returns the list; bad value → 400 and Seed not called; "Seeding, no limit" line.
- [ ] Keys: `seed`, `seed.title`, `seed.message`, `seed.24h`, `seed.72h`, `seed.none`, `downloads.seeding_no_limit` (EN + BG).
- [ ] Commit `feat(web): Seed button on finished downloads`.
