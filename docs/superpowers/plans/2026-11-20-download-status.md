# Download Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every downloading or seeding card shows upload speed, uploaded, ratio and peers. A stuck download says why and offers "Start now" or "Download in any order".

**Architecture:** The Transmission client reads more fields and gains `StartNow`, `SetSequential(on)` and `PortOpen`. The downloads service adds the live stats and a computed `Stuck` reason to `Download`, remembers `in_order` per download, and caches the port test. The handlers add two POST routes, and the template adds the status line.

**Tech Stack:** Go, gin, html/template, pgx/goose, testify/goquery.

**Spec:** `docs/superpowers/specs/2026-11-20-download-status-design.md`

## Global Constraints

- Stuck = state "downloading", not paused, live, `RateDownload == 0`, `PeersSending == 0`.
- Reason order: queued, then tracker error, then no peers (with the port test), then in-order on, then plain "none sending".
- Seeders = the highest tracker `seederCount` ≥ 0. -1 means not known.
- Port test: at most once per 10 minutes, and only when some download is stuck with no peers.
- en and bg strings for everything new.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- A magnet whose details haven't arrived yet (Size 0) and has no peers shows "no peers". It doesn't crash, and it doesn't fire the port test every poll (Task 2 test: `TestPortTestThrottled`).
- A download marked any-order stays any-order across polls (Task 2: `TestPollKeepsAnyOrder`).
- A Transmission failure in `port-test` doesn't break List: the port note is just left out (Task 2: `TestPortTestErrorIgnored`).
- A paused or seeding download is never shown as stuck (Task 2: `TestStuckOnlyWhileDownloading`).
- The status line doesn't cause horizontal scrolling at 320px (Task 3 visual check).

---

### Task 1: Transmission client

**Files:**
- Modify: `internal/transmission/client.go`
- Test: `internal/transmission/client_test.go`

**Interfaces:**
- Produces:
  - New `Torrent` fields:
    - `RateUpload int64`
    - `Uploaded int64` (from `uploadedEver`)
    - `Ratio float64` (`uploadRatio`, with negatives becoming 0)
    - `Peers int` (`peersConnected`)
    - `PeersSending int` (`peersSendingToUs`)
    - `Queued bool` (status 3)
    - `QueuePos int` (`queuePosition` + 1)
    - `Seeders int` (-1 when not known)
    - `TrackerError string`: the first tracker's `lastAnnounceResult` when its `lastAnnounceSucceeded` is false and `hasAnnounced` is true.
  - `StartNow(ctx, hash) error`
  - `SetSequential(ctx, hash string, on bool) error`
  - `PortOpen(ctx) (bool, error)`

- [ ] **Step 1: Failing tests:**
  - `TestGetStats`: the fake RPC answers with `rateUpload` 1000, `uploadedEver` 5e9, `uploadRatio` 0.5, `peersConnected` 3, `peersSendingToUs` 0, `status` 3, `queuePosition` 1, and `trackerStats` [{seederCount -1}, {seederCount 45, hasAnnounced true, lastAnnounceSucceeded false, lastAnnounceResult "Connection failed"}]. Expect every field: `QueuePos` 2, `Seeders` 45, `TrackerError` "Connection failed", `Queued` true.
  - `TestGetRatioUnknown`: `uploadRatio` -1 gives `Ratio` 0, and no trackers gives `Seeders` -1.
  - `TestStartNow`: sends method `torrent-start-now` with the ids.
  - `TestSetSequentialOff`: sends `sequential_download: false`.
  - `TestPortOpen`: `port-test` answers `{"port-is-open": true}` and the result is true.
- [ ] **Step 2:** `go test ./internal/transmission/` fails.
- [ ] **Step 3: Implement.** Add the fields to `getFields` and `rpcTorrent`.
- [ ] **Step 4:** Tests pass. Fix the callers of `SetSequential` (the service and its fake) to pass `true`.
- [ ] **Step 5:** `make test` passes. Commit `feat(transmission): upload, peer, queue and tracker stats; start now; port test`.

### Task 2: Service: stats, stuck reasons, any-order, port test

**Files:**
- Create: `internal/db/migrations/00004_download_in_order.sql` (`in_order BOOLEAN NOT NULL DEFAULT true`)
- Modify:
  - `internal/db/downloads.go`: `InOrder bool`, in `downloadCols`, the scan and the update.
  - `internal/downloads/service.go`
  - the `Torrents` interface: `StartNow`, `SetSequential(on)` and `PortOpen`.
- Test:
  - `internal/downloads/service_test.go`
  - `internal/db` test for the column round-trip

**Interfaces:**
- Consumes: the Task 1 fields and methods.
- Produces:
  - New `Download` fields:
    - `RateUpload, Uploaded int64`
    - `Ratio float64`
    - `Peers, PeersSending, Seeders int`
    - `InOrder bool`
    - `Live bool` (Transmission knows it)
    - `Stuck string`: "" or one of "queued", "tracker", "no_peers", "in_order", "not_sending"
    - `QueuePos int`
    - `TrackerError string`
    - `PortClosed bool`
    - `Port int`
  - Service methods:
    - `StartNow(ctx, hash) error`
    - `AnyOrder(ctx, hash) error`: calls `SetSequential(false)` and stores `InOrder = false`.
  - Service fields:
    - `PortEvery time.Duration` (default 10m)
    - `Port int` (shown in the note; set from config `TRANSMISSION_PEER_PORT`, default 51413)

- [ ] **Step 1: Failing tests:**
  - **Stuck reasons:** `TestStuckReasons`, a table test (each case one torrent). The reason order is the constraint above. Queued gives "queued" with `QueuePos`. A `TrackerError` gives "tracker". Peers 0 gives "no_peers". Peers 3 with none sending and in-order on gives "in_order". The same with `InOrder` false gives "not_sending". A rate of 50 gives "".
  - **Never stuck while not downloading:** `TestStuckOnlyWhileDownloading`: paused, or state "filed", gives "".
  - **Any-order sticks:** `TestPollKeepsAnyOrder`: after `AnyOrder`, a Poll with a non-sequential torrent makes no `SetSequential(true)` call. `TestAnyOrder` records `SetSequential(hash, false)` and the row has `InOrder` false.
  - **Start now:** `TestStartNow` records the call.
  - **Port test:** `TestPortTestThrottled`: two Lists 1 minute apart with "no_peers" make one `PortOpen` call, and with the clock 11 minutes later, a second one. No stuck-no-peers download means zero calls. `PortOpen` false gives `PortClosed` true on the "no_peers" row only. `TestPortTestErrorIgnored`: a `PortOpen` error means List still succeeds with `PortClosed` false.
  - **Stats copied:** `TestListStats`: every new field is copied from the torrent.
- [ ] **Step 2:** `make test` fails.
- [ ] **Step 3: Implement.**
  - `stuckReason(d Download, t Torrent) string` is a pure function.
  - The port cache lives on the Service, behind its own mutex, with `lastPortCheck time.Time` and `portOpen bool`.
  - `Poll` only sets sequential when `r.InOrder`.
- [ ] **Step 4:** `make test` and `make check` pass.
- [ ] **Step 5:** Commit `feat(downloads): live stats, why a download is stuck, any-order and start-now`.

### Task 3: Page

**Files:**
- Modify:
  - `internal/handlers/deps.go` (`StartNow` and `AnyOrder` in the interface), `downloads.go` (`postStartNow`, `postAnyOrder`, both reusing `pauseOrResume`), `web.go` (routes `POST /downloads/:hash/start-now` and `/any-order`), and the cmd wiring for `Port`.
  - `internal/templates/pages/downloads.html`
  - `internal/static/css/media.css`
  - `internal/i18n/en.json` and `bg.json`
- Test: a new `internal/handlers/downloads_test.go` (or the existing pages tests), with the fakes in `fakes_test.go`.

**Interfaces:**
- Consumes: the Task 2 `Download` fields and methods.
- Produces: markup.
  - A `p.download-stats` line, shown when `Live` and (downloading or seeding): `↑ {rate} · {size} up · ratio {%.2f} · {Peers} of {Seeders} peers, {PeersSending} sending`. The "of N" part is left out when `Seeders` < 0.
  - When `Stuck` is set, `p.download-stats.stuck` carries the reason text. Its buttons are `button.start-now` (queued) or `button.any-order` (in_order), with `hx-post` and `hx-target="#downloads-list"`, plus `small.any-order-note`.

- [ ] **Step 1: Failing tests:**
  - `TestDownloadStatsLine`: the text contains "ratio 0.50" and "3 of 45 peers, 1 sending".
  - `TestStuckQueuedButton`: `.stuck` contains "#2", and `button.start-now[hx-post=/downloads/<h>/start-now]` exists.
  - `TestStuckInOrderButton`: `button.any-order` and `.any-order-note` exist.
  - `TestStuckNoPeersPort`: the text contains "51413" when `PortClosed`.
  - `TestPostAnyOrder` and `TestPostStartNow`: each calls the fake, and the answer is the list.
  - An i18n test (if one exists) keeps the en and bg keys in step.
- [ ] **Step 2:** `make test` fails.
- [ ] **Step 3: Implement.** The template, CSS (muted small text, `.stuck` amber, wrapping, no overflow), i18n, routes and wiring.
- [ ] **Step 4:** `make test` and `make check` pass. Visual check at 320px and desktop through the scratchpad harness: dump the rendered list with a fake service.
- [ ] **Step 5:** Commit `feat(web): download stats line and stuck reasons with fixes`.
