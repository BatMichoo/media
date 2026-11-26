# Subtitle size: design (short)

## Why

Subtitles show at one fixed size (`video::cue { font-size: 1.4em }`). On a
far-away TV or a small phone that size is wrong for someone, and they want
to set it themselves.

## Goals

- In the player's ⚙ menu, a "Subtitle size" row: `−  100%  +`. Each press
  changes the size by 10%, between 50% and 250%; 100% is today's 1.4em.
- The change shows at once, without restarting the stream.
- Saved per person (follows them to other devices, the TV app included).

## Non-goals / limits

- Picture subtitles (PGS, VobSub) are burned in by Jellyfin; their size
  can't change, so the row is hidden while one is chosen.
- iPhone fullscreen uses iOS's own subtitle settings.

## Approaches

1. **± buttons, saved per person on the server** (chosen by the person).
2. Four named sizes (Small … Extra large): fewer steps, but the person
   asked for + and −.
3. A slider: fiddly with a TV remote. Per-browser storage: wouldn't follow
   the person to the TV.

## Design

- Migration 00008: `preferences.subtitle_size INTEGER NOT NULL DEFAULT 100`.
- `db.Prefs` / `watching.Prefs` gain `SubtitleSize int` (0, meaning no
  saved row, reads as 100). `SetPrefs` takes a third optional value.
- `POST /prefs` accepts `subtitle_size`: a whole number from 50 to 250 and
  a multiple of 10; anything else is 400.
- The watch page writes the size onto the player as `data-sub-size`.
- `player.js` keeps one `<style>` element with
  `#player video::cue { font-size: calc(1.4em * N / 100) }` (a rule, since
  `::cue` doesn't read custom properties everywhere); − and + change N by
  10, update the label, disable at the ends, and save through `/prefs`.
- The buttons are ordinary menu buttons so the TV remote's arrows and OK
  reach them; labels "Subtitle size", "Smaller", "Larger" in en and bg.

## Left to the builder

Exact markup and styling of the row.
