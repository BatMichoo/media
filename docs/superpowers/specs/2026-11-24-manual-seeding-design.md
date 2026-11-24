# Manual Seeding (short design)

Date: 2026-11-24. Approved by the user the same day.

## Why

A finished torrent seeds for 72 hours after it completes, then the app
stops it for good: the Downloads page says "Seeding done" and offers no
way to seed it again, and a torrent paused past its 72 hours stays stopped
on Resume. The user wants to start seeding again by hand.

## Goals

- A "Seed" button on finished downloads that aren't seeding: those showing
  "Seeding done", which includes paused ones whose time ran out.
- The button opens a sheet (like Remove) with three choices: **24 hours**,
  **72 hours**, **No limit**. The time counts from the click.
- While seeding: "Seeding until <date>", or "Seeding, no limit". Pause and
  Resume keep working; Resume continues the chosen time if it hasn't
  passed yet.
- When the chosen time is up, the app stops it and it shows "Seeding done"
  with the Seed button again. "No limit" seeds until paused.
- EN and BG.

## Non-goals

- Extending a torrent that is still seeding (can come later).
- Ratio limits.

## Anti-goals

- The automatic 72 hours after finishing must not change.
- Files and the library are untouched.
- A restart must not lose the chosen time (it is stored, not in memory).

## Approaches

1. **Store the chosen end in the database; the existing poll stops it
   (chosen).** The poll already stops torrents past their time; it reads
   the end from the row when one was chosen, else the finish date + 72 h.
2. Transmission's own seed limits. Rejected: they are share-ratio and
   idle-time limits, not a wall-clock end, and the app already owns
   stopping.

## Decisions

- New columns on downloads: `seed_until` (nullable time) and
  `seed_no_limit` (bool). A chosen time replaces the automatic one for
  that torrent.
- Who can do it: anyone who can pause (any signed-in user).
- Only a finished torrent Transmission still has can be seeded again.

## Left to the builder

- Names, the form field values, the exact BG wording.
- Tests: choosing 24 h/72 h/no limit sets the end and starts the torrent;
  the poll stops it after the chosen end and not before; no limit is never
  stopped by the poll; Pause/Resume inside the chosen time; Resume after
  it leaves it stopped; refused for unfinished or unknown torrents; the
  button and sheet render; the page text for both seeding lines.
