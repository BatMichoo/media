# Browser Player Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the browser player fit one row on phones and feel smooth: icon controls, ⚙ menu, touch gestures, a better seek bar, spinner, mute/volume, and fullscreen that works on iPhone.

**Architecture:** Extend the existing vanilla player: `watch.html` markup, `media.css` player block, `player.js`. The Go side only gains four labels. There is no JS test setup, so JS behaviour is verified in a real browser against a local harness page with a mocked `/play`.

**Tech Stack:** Go (gin, html/template, goquery tests), vanilla ES5-style JS (match the existing `var`/function style), CSS, hls.js (unchanged).

**Spec:** `docs/superpowers/specs/2026-11-19-player-controls-design.md`

## Global Constraints

- No new libraries. hls.js stays the only one.
- No change to `/play`, `/progress`, `/prefs` request shapes or the burned-in subtitle / audio-restart logic.
- Out of scope: playback speed, picture-in-picture, Chromecast, seek thumbnails.
- Bottom bar never wraps, down to a 320px width. Touch targets ≥ 44px. Seek hit area 24px tall.
- Centre cluster breakpoint: `(pointer: coarse), (max-width: 700px)`.
- Single tap is decided after 250ms. Idle hide after 3000ms while playing.
- Controls stay visible while the menu is open or the seek bar is dragged.
- localStorage access is wrapped in try/catch.
- Existing keys keep working: Space/K toggle, ←/→ ±10s, F fullscreen. Add M for mute and Escape to close the menu.

## Review Focus

- A part with no `duration` yet (0) while dragging the seek bar must not produce NaN positions or seek to NaN. Clamp to 0 when the duration is 0 (Task 3 harness check).
- Switching to a picture subtitle reloads `/play`. The menu must reflect the new session, and the spinner must clear on success *and* on error (Task 2 and Task 5 checks).
- A double tap must not also fire the single-tap toggle of the controls (Task 4 check).
- Leaving fullscreen with the system gesture or Esc instead of our button must still swap the icon back and unlock orientation, so listen to `fullscreenchange` / `webkitendfullscreen` (Task 5 check).
- A browser with localStorage blocked (private mode) must still play with volume 1, unmuted (Task 5 check).

---

### Task 1: Player labels

**Files:**
- Modify: `internal/handlers/player.go:39-40` (`playerLabels`)
- Modify: `internal/i18n/en.json`, `internal/i18n/bg.json` (after `player.fullscreen`)
- Test: `internal/handlers/player_test.go` (`TestWatchPageData`)

**Interfaces:**
- Produces: the `data-labels` JSON gains keys `mute`, `unmute`, `settings`, `exitFullscreen` (JS reads `L.mute` and so on).

- [ ] **Step 1: Add assertions to `TestWatchPageData`**

```go
var labels map[string]string
require.NoError(t, json.Unmarshal([]byte(attr("data-labels")), &labels))
for _, k := range []string{"mute", "unmute", "settings", "exitFullscreen", "subtitles", "audio", "off"} {
	assert.NotEmpty(t, labels[k], k)
	assert.NotEqual(t, "player."+k, labels[k], k) // i18n returns the key when missing
}
```

- [ ] **Step 2: Run it.** `make test` (or `go test ./internal/handlers -run TestWatchPageData` with `TEST_DATABASE_URL` set). Expect FAIL on `mute`.
- [ ] **Step 3: Add the labels.** en: Mute / Unmute / Settings / Exit fullscreen. bg: Без звук / Със звук / Настройки / Изход от цял екран. Append the four keys to `playerLabels`.
- [ ] **Step 4: Run `make test` and `make check`.** Expect PASS.
- [ ] **Step 5: Commit** `feat(web): player labels for mute, settings, exit fullscreen`

### Task 2: New markup, layout, icons and ⚙ menu

**Files:**
- Modify: `internal/templates/pages/watch.html`
- Modify: `internal/static/css/media.css:173-198`
- Modify: `internal/static/js/player.js`
- Test: `internal/handlers/player_test.go` (`TestWatchPageData`)
- Harness (not committed): `$SCRATCH/harness/` (see Step 6)

**Interfaces (markup contract, used by Tasks 3–5):**
- An inline SVG sprite at the top of `#player` with symbols `i-play`, `i-pause`, `i-back10`, `i-fwd10`, `i-volume`, `i-muted`, `i-settings`, `i-fullscreen`, `i-exit-fullscreen`, `i-back`. Buttons hold `<svg><use href="#i-…"/></svg>`.
- `.player-center` holds `[data-action=back10]`, `[data-action=toggle].player-toggle`, `[data-action=forward10]`. A second set sits in `.player-bar .player-skip`. CSS shows `.player-center` and hides `.player-skip` under the breakpoint, and the reverse otherwise.
- `.player-timeline` is `span.player-current`, `div.player-seek[role=slider][tabindex=0][aria-label]` (children `.player-buffered`, `.player-played`, `.player-thumb`, `.player-bubble[hidden]`), `span.player-total`.
- `.player-bar` is `.player-skip`, `[data-action=mute]`, `input.player-volume[type=range][min=0][max=1][step=0.05]`, `.player-spacer`, `[data-action=settings][aria-haspopup=true][aria-expanded=false]`, `[data-action=fullscreen]`.
- `div.player-menu[hidden]` holds `section[data-group=subtitles]` and `section[data-group=audio]`, each with an `h3` and a `ul`. Items are `button[data-track="off"|"<index>"][role=menuitemradio][aria-checked]`.
- `div.player-spinner[hidden]`, `div.player-flash.left[hidden]`, `div.player-flash.right[hidden]`.
- JS helper `icon(button, name)` sets the `<use>` href to `#i-<name>`. `toggles` is the list of all `[data-action=toggle]`.
- JS `menuOpen()` returns a boolean, and `closeMenu()`.
- The `<select>` elements are removed.

- [ ] **Step 1: Add markup assertions to `TestWatchPageData`.**

```go
assert.Equal(t, 0, p.Find("select").Length())
assert.Equal(t, 1, p.Find(`.player-seek[role="slider"]`).Length())
assert.Equal(t, 2, p.Find(`[data-action="toggle"]`).Length())
for _, a := range []string{"mute", "settings", "fullscreen"} {
	assert.Equal(t, 1, p.Find(`[data-action="`+a+`"]`).Length(), a)
}
assert.Equal(t, 1, p.Find(`.player-menu [data-group="subtitles"]`).Length())
assert.Equal(t, 1, p.Find(`.player-menu [data-group="audio"]`).Length())
assert.Equal(t, 1, p.Find(".player-spinner").Length())
```

- [ ] **Step 2: Run `make test`.** Expect FAIL.
- [ ] **Step 3: Rewrite `watch.html`** to the contract above. Keep all the `data-*` attributes on `#player`. Use i18n for every aria-label and the `h3`s.
- [ ] **Step 4: Rewrite the player CSS block.** `.player-bar` uses `flex-wrap: nowrap`. Buttons are 44×44 icon-only (`svg` 24px, `fill: currentColor`). The centre cluster is absolutely centred with a 64px toggle. The menu panel sits bottom-right above the bar (`max-height: 60vh; overflow:auto`). `.player-volume` is hidden under `(pointer: coarse)`. Keep the idle rule, and add `.player.menu-open` and `.player.seeking` so idle never hides the controls in those states.
- [ ] **Step 5: Re-wire `player.js`.** `fillTracks(s)` builds the menu lists instead of selects (same chosen logic). Hide the audio section when there are fewer than 2 tracks. Choosing an item runs the old subtitle / audio change logic (move the bodies into `chooseSubtitle(index|null)` / `chooseAudio(index)`) and then `closeMenu()`. The settings button toggles the menu and `aria-expanded`. A tap outside the menu or Escape closes it. Play/pause swaps the icon and aria-label on all `toggles`. The time labels fill `.player-current` / `.player-total`. Keep the old range seeking temporarily out of the way: Task 3 replaces it, so for now the slider only displays progress.
- [ ] **Step 6: Build the harness.** Put it in a new dir `$SCRATCH/harness/` (scratchpad). Make `sample.mp4` with `ffmpeg -f lavfi -i testsrc=size=1280x720:rate=25 -f lavfi -i sine -t 120 -c:v libx264 -c:a aac -shortest sample.mp4`, and a `sample.vtt`. `index.html` holds the HTML rendered by the real template: dump it with a throwaway `go test` print, or copy it from a running dev server. `/static/media/...` paths map to the repo's `internal/static` (symlink). Add an inline script before `player.js` that stubs `window.fetch`: `/play/*` returns `{direct:true,url:"sample.mp4",duration:120,sub:null,audio:null,playSessionId:"x",mediaSourceId:"x",subtitles:[{index:3,label:"English",lang:"eng",url:"sample.vtt"},{index:4,label:"PGS",lang:"bul",image:true}],audioTracks:[{index:1,label:"English",lang:"eng"},{index:2,label:"Bulgarian",lang:"bul"}]}`, and anything else returns `{ok:true}`. Serve it with `python -m http.server`.
- [ ] **Step 7: Browser check (claude-in-chrome) at 360×740 and 1280×800.** The bottom bar is one row at 360px, the centre cluster shows only at 360px, the menu opens and closes (outside tap, Escape), choosing PGS calls `/play` again and the menu shows PGS checked, the audio section is visible (2 tracks).
- [ ] **Step 8: Run `make test` and `make check`.** Expect PASS. **Commit** `feat(web): compact player layout with icons and a settings menu`

### Task 3: Seek bar

**Files:**
- Modify: `internal/static/js/player.js`, `internal/static/css/media.css`

**Interfaces:**
- Consumes: the `.player-seek` children from Task 2, and `duration()`.
- Produces: `dragging` (boolean) and `setSeekUI(seconds)` (positions the played fill and thumb, sets aria-valuenow/aria-valuetext to `clock(seconds)`).

- [ ] **Step 1: Implement.** `pointerdown` on `.player-seek` calls `setPointerCapture`, sets `dragging=true`, and adds `.seeking` on root. `pointermove` maps x to `fraction × duration()`, clamps to [0, duration()] (0 when the duration is 0), calls `setSeekUI`, and shows `.player-bubble` with `clock(t)` above the thumb. `pointerup`/`pointercancel` set `video.currentTime` once (pointerup only), hide the bubble, and reset `dragging`. `timeupdate` skips `setSeekUI` while dragging. `progress` and `timeupdate` set the `.player-buffered` width from the `video.buffered` range containing currentTime. On a focused slider, ←/→ call `jump(∓10)`. The document handler already does this, so make sure it fires once, not twice. Home/End go to 0/end.
- [ ] **Step 2: Harness check at 360px, with real touch emulation.** Dragging shows the bubble and the video does not seek until release (check `currentTime` via javascript_tool during the drag). The buffered fill is visible. Set the stub `duration:0` and confirm dragging gives no NaN in aria-valuetext.
- [ ] **Step 3: Commit** `feat(web): player seek bar seeks on release and shows buffered`

### Task 4: Gestures and idle

**Files:**
- Modify: `internal/static/js/player.js`, `internal/static/css/media.css`

**Interfaces:**
- Consumes: `menuOpen()`, `closeMenu()`, `dragging`, `jump(s)`, `toggle()`, `fullscreen()`.
- Produces: `showControls()`, `hideControls()`, `flash(side, text)`.

- [ ] **Step 1: Implement.** Replace the video-click handler with `pointerup` on the video and the empty overlay area (not on controls). For `pointerType === "touch"`: if a second tap lands within 250ms, run the double tap (left third `jump(-10)` + `flash("left","−10")`, right third `jump(10)` + `flash("right","+10")`, middle `fullscreen()`) and cancel the pending single tap. Otherwise, after 250ms, run the single tap: if the menu is open, close it. Otherwise toggle visibility (`hideControls` if visible, else `showControls`). For a mouse: a click toggles play, and `dblclick` toggles fullscreen (cancel the click's toggle the same way with the 250ms timer). `flash` shows the bubble for 600ms, and repeated double taps add up ("−20"). The idle timer checks `!video.paused && !menuOpen() && !dragging`. Touch `pointerdown` no longer calls `wake()` (a tap decides visibility itself), but mousemove and keys still do.
- [ ] **Step 2: Harness check with touch emulation.** A single tap while playing hides the controls and does not pause. Another tap shows them. A double tap on the right gives +10s, shows the bubble, and does not toggle the controls. A triple-quick tap gives "+20". With the menu open, the controls don't auto-hide after 3s. On desktop, a click pauses and a double click goes fullscreen without leaving the play state flipped.
- [ ] **Step 3: Commit** `feat(web): player tap and double-tap gestures`

### Task 5: Spinner, mute/volume, fullscreen

**Files:**
- Modify: `internal/static/js/player.js`, `internal/static/css/media.css`

**Interfaces:**
- Consumes: `icon()`, `L.mute`/`L.unmute`/`L.exitFullscreen`/`L.fullscreen`.
- Produces: `busy(on)`.

- [ ] **Step 1: Spinner.** `busy(true)` on `load()` start and on `waiting`/`stalled`. `busy(false)` on `playing`, `canplay`, `pause`, `showError()`. The spinner is a CSS-only rotating ring, centred, with `pointer-events:none`. Hide the centre toggle while busy.
- [ ] **Step 2: Volume.** The mute button and the M key toggle `video.muted`, swap the `i-volume`/`i-muted` icon and the aria-label. `input.player-volume` sets `video.volume`, and moving it above 0 unmutes. Save `{muted, volume}` under localStorage key `media.player.volume` on `volumechange` and restore it at start, each in try/catch.
- [ ] **Step 3: Fullscreen.** `fullscreen()`: if `root.requestFullscreen` exists, use it (or exit). Otherwise, if `video.webkitEnterFullscreen` exists, call it. After entering, `screen.orientation && screen.orientation.lock && screen.orientation.lock("landscape").catch(noop)`, and on exit `screen.orientation.unlock()` in try/catch. Update the icon and label from `document.fullscreenchange` plus the video's `webkitbeginfullscreen`/`webkitendfullscreen`. Hide the fullscreen button when neither API exists.
- [ ] **Step 3b: Harness checks.** Throttle the network (or stub `/play` with a 1s delay) and see the spinner, then it clears. Make `/play` return 500: the error shows and the spinner is gone. Mute, reload, still muted. Run `localStorage` blocked (javascript_tool: override `Storage.prototype.getItem` to throw) and the page still plays. Enter fullscreen, press Esc, and the icon is back to "enter".
- [ ] **Step 4: Run `make check`. Commit** `feat(web): player spinner, mute and volume, iPhone fullscreen`

### Task 6: Final pass

- [ ] Full harness walk at 320px, 360px and 1280px in both orientations: one-row bar, no horizontal scroll, keyboard (Space, K, ←, →, F, M, Esc) works on desktop, and Tab reaches every control with a visible focus ring.
- [ ] `make test`, `make check`. Expect PASS.
- [ ] Ask the user to try it on their real phone (iPhone fullscreen and orientation lock can't be fully emulated).
