# Download status: design (short)

## Why

The Downloads page shows progress, speed and time left, but nothing about
uploading, and nothing about why a torrent isn't moving. A torrent with 45
seeders stalled and there was no way to tell why from the app.

## Goals

- Each downloading or seeding card gets one muted line:
  `↑ 410 KB/s · 1.3 GB up · ratio 0.26 · 12 of 45 peers, 8 sending`.
- When a download is stuck (downloading, not paused, 0 B/s, nobody sending,
  on the current poll), the line turns amber and gives the first reason that
  applies:
  1. **Queued** (Transmission status 3): "Queued (#N)", with a **Start now**
     button (`torrent-start-now`).
  2. **Tracker error**: "Can't reach tracker: <message>".
  3. **No peers connected**: "No peers connected (tracker sees N seeders)",
     plus "port P is closed" when Transmission's port test says so.
  4. **Peers connected, none sending, in-order on**: "Peers connected but
     none sending", with a **Download in any order** button and a note that
     watching won't work until it finishes.
  5. Otherwise: "Peers connected but none sending".
- "Download in any order" is remembered per download (`in_order` column,
  default true); the poll no longer turns in-order back on for those.

## Non-goals

- A per-torrent details sheet (option B, rejected: the user wants it on the
  card).
- Automatic fixes (option C, rejected: it would quietly break watching
  while downloading).
- Changing Transmission's settings (queue size, port).

## Approaches

- Info: A, always on the card (chosen), over B, a short line plus a details
  sheet.
- Fixes: B, a button on the stuck card (chosen), over A, explain only, and
  C, automatic.

## Decided

- Seeders shown = the highest `seederCount` among the trackers (-1 / unknown
  ignored); left out when no tracker knows.
- Port test (`port-test` RPC) runs at most every 10 minutes and only while
  some download is stuck with no peers; the last answer is cached.
- Strings in en and bg.

## Left to the builder

Exact wording and colours, how the line wraps at 320px, and the wording
of the any-order note.
