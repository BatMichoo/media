# Browser player controls: design

## Why

On phones the fullscreen button wraps onto a second row, and the controls
feel clunky. Tapping the video to see the controls pauses it. Dragging the
seek bar stutters. There is no buffering feedback, no double-tap skip, no
mute, and fullscreen does not work on iPhone. All of these are in scope.

## Goals

- One-row bottom bar at any width, down to 320px.
- Touch: a tap shows or hides the controls, a double tap on the left or
  right third skips 10s.
- A seek bar that is easy to grab, shows buffered and played progress,
  previews the time while dragging, and seeks once on release.
- A spinner while buffering.
- A mute button, plus a volume slider on desktop.
- Fullscreen that works on iPhone and turns to landscape where allowed.

## Non-goals

- No new libraries. hls.js stays the only one.
- No change to `/play`, `/progress`, `/prefs`, or how subtitle and audio
  switching works (picture subtitles are still burned in through a new
  `/play`).
- No playback speed, picture-in-picture, Chromecast, or thumbnails in the
  seek preview.

## Anti-goals

- Controls that hide while someone is using them (menu open, dragging).
- Anything that breaks the current keyboard use on desktop or TV.

## Approaches

- **A. Improve our own controls (chosen).** Extend the vanilla JS, CSS and
  template. No dependency, full control, and it fits our unusual track
  switching.
- B. A player library (Plyr or Vidstack). Polished, but it clashes with
  burned-in subtitles and audio changes that restart the stream, and it
  adds about 100KB plus a style to override.
- C. Native controls on mobile. Least work, but it loses our subtitle and
  audio menu on iOS and splits the code into two layouts.

## Design

**Layout.** The top bar has back and the title. A centre cluster has
⟲10, play/pause and 10⟳, shown on touch devices (`pointer: coarse`) or
narrow screens (≤700px). Elsewhere those three buttons sit at the start of
the bottom bar. The bottom section has a time row (`current ━━●── total`)
and an icon row (mute, [volume on desktop], spacer, ⚙, ⛶) that never wraps.
Inline SVG icons replace the Unicode glyphs. Touch targets are at least
44px.

**Settings menu (⚙).** A small panel above the bar with a Subtitles list
and an Audio list (Audio only with 2 or more tracks). Items are buttons
with a check mark on the current choice. It closes on choosing, on a tap
outside, or on Escape. The `<select>` elements go away, and the same
change logic runs as today.

**Gestures.** On touch, a single tap (decided after 250ms with no second
tap) shows the controls if hidden and hides them if shown. A double tap on
the left or right third skips -10s or +10s and shows a brief "−10" or "+10"
bubble on that side. A double tap in the middle third toggles fullscreen.
On a mouse, a click toggles play and a double click toggles fullscreen.

**Seek bar.** A custom `role="slider"` element (focusable, arrow keys ±10s,
with aria-valuenow and aria-valuetext) holding layers for the track,
buffered and played portions, and a thumb. Its hit area is 24px tall even
though the bar looks thin. Dragging with pointer capture moves the thumb
and a time bubble above it, and `currentTime` is set once on release.
While dragging, timeupdate does not move the thumb.

**Spinner.** Shown on `waiting`/`stalled` and while loading a new `/play`,
hidden on `playing`/`canplay`/error.

**Volume.** The mute button toggles `video.muted`, and the M key does the
same. The volume slider is hidden on coarse pointers (iOS ignores volume).
Mute and volume are remembered in localStorage, wrapped in try/catch.

**Fullscreen.** Use `root.requestFullscreen` when available. Otherwise use
`video.webkitEnterFullscreen` (iPhone, native UI; text subtitle tracks
still show). After entering, try `screen.orientation.lock("landscape")`,
and unlock on exit. The icon swaps to "exit fullscreen". Hide ⛶ if neither
API exists.

**Idle.** Controls hide after 3s of playing without input, except while
the menu is open or the seek bar is being dragged.

**Texts.** New `player.*` labels for mute, unmute, settings and exit
fullscreen in `en.json` and `bg.json`, added to `playerLabels`.

## Left to the builder

Exact icon paths, spacing and animation timings, and the CSS breakpoint
details within the rules above.
