# Inline player: design

## Why

The watch page takes over the whole window and has its own ← back bar, so
it feels like leaving the app for a separate screen. On a phone it doesn't
feel like the rest of the app.

## Goals

- The watch page is a normal page in the app's shell (header and bottom
  nav), with the player in a 16:9 box at the top. There is no ← back bar.
- Below the player:
  - **Movie:** title, year and overview.
  - **Episode:** show name, "S1 · E2 Name" and overview, then that
    season's episodes. Each is a `/watch/<id>` link, the playing one is
    marked, and watched ones are marked the same way as on the show page.
- Leaving through the header or bottom nav, which swap the page with
  htmx, saves the final position and ends the session the same way a page
  unload does.

## Non-goals

- Switching episodes without reloading the page (approach B).
- Changes to the shared theme.
- Auto-playing the next episode.

## Approaches

- **A. Normal page with a boxed player that cleans up when htmx swaps it
  out (chosen).** Feels like the rest of the app, with one well-defined
  hook and no theme changes.
- B. The same, but switching episodes in place. Rejected: much more
  JavaScript for an occasional tap.
- C. Full page loads for nav on the watch page. Rejected: it needs theme
  changes or hacks, and it's slower.

## Design

- **Template** (`watch.html`):
  - `.watch` holds `#player` (same data attributes, minus the top bar) and
    `.watch-info` below it.
  - The player keeps its controls, gestures, ⚙ menu, seek bar and spinner.
    The centre play cluster, the bottom bar and the menu are positioned
    inside the box.
- **Box styling** (`media.css`):
  - `.player` stops being `position: fixed; inset: 0`. It becomes
    `position: relative; aspect-ratio: 16 / 9; width: 100%; max-height:
    75vh`, black, with rounded corners on desktop and edge to edge on
    phones (no side padding).
  - In fullscreen (`.player:fullscreen`, plus the webkit prefix), it is
    100vw × 100vh with no radius.
  - Controls auto-hide as today.
  - The menu's computed `bottom` already works relative to the box.
  - The title in the info section replaces the old top-bar title.
- **Handler** (`watch`):
  - Adds the info fields: kind, year, overview, show name, season and
    episode numbers.
  - For an episode, it adds the season's episodes (from
    `Catalog.Episodes(seriesID)`, filtered to its `ParentIndex`) with
    watched marks from `Watching.Many`.
  - `Back` and the back link go away. The player's `data-back` stays and
    is used only when a movie or the last part ends.
  - When the last part of an episode ends, the player goes to the show
    page, as today.
- **Player lifecycle** (`player.js`):
  - **Start-up as a function:** it runs at load and again whenever htmx
    inserts a `#player` (scripts in swapped content run). A
    `root.dataset.ready` flag prevents a second start on the same element.
  - **Clean-up:** every document, window and screen listener is
    registered through a helper and recorded. A `teardown()` function
    removes them, saves `stop`, destroys hls and clears the timers.
  - **When it runs:** `teardown()` runs on `htmx:beforeSwap` when the swap
    target contains the player, and on `pagehide` as today.
  - **History:** the player element gets `hx-history="false"`, so htmx
    never restores a stale snapshot of a playing video. Going back to the
    watch page asks the server again.
- **Mobile:**
  - The page keeps the app's header and bottom nav.
  - The player box is full width, with the info section in the normal
    page gutter below it.
  - Nothing horizontal scrolls at 320px.

## Left to the builder

Exact spacing, how the "playing" episode row looks (reuse the
`.episode` rows from the show page), and the desktop width of the box
(about 1200px max, centred).
