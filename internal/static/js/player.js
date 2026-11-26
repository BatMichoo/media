// The media app's player: asks the app how to play each part in this
// browser (/play), plays it directly or as HLS (hls.js where needed),
// saves the place every 10 seconds (/progress), and remembers subtitle
// and audio languages (/prefs).
//
// The watch page can arrive and leave through htmx page swaps (the app's
// header and bottom nav), not only page loads: a player starts whenever a
// #player appears and cleans up (last place saved, stream stopped,
// listeners removed) before a swap takes it away.
(function () {
  "use strict";
  // htmx runs this file again each time it swaps the watch page in; one
  // copy does the work.
  if (window.mediaPlayer) {
    window.mediaPlayer.boot();
    return;
  }
  var stop = null;    // the running player's teardown
  var running = null; // its root

  function halt() {
    var s = stop;
    stop = null;
    running = null;
    if (s) s();
  }

  // sweep stops a player whose page went away without a swap event (a
  // history restore swaps the page in directly).
  function sweep() {
    if (stop && running && !running.isConnected) halt();
  }

  function boot() {
    sweep();
    var root = document.getElementById("player");
    if (!root || root.dataset.ready) return;
    if (stop) halt();
    root.dataset.ready = "1";
    running = root;
    stop = start(root);
  }

  // Going back to the watch page reloads it rather than restoring a copy
  // htmx kept (the player opts out of that copy, so htmx would otherwise
  // fetch the whole page and run every script on it again).
  function configure() {
    if (window.htmx) window.htmx.config.refreshOnHistoryMiss = true;
  }

  document.addEventListener("htmx:beforeSwap", function (e) {
    var target = e.detail && e.detail.target;
    if (stop && running && target && target.contains(running)) halt();
  });
  document.addEventListener("htmx:afterSwap", boot);
  document.addEventListener("htmx:historyRestore", function () { sweep(); boot(); });
  document.addEventListener("DOMContentLoaded", configure);
  window.mediaPlayer = { boot: boot };
  configure();
  boot();

  // start runs a player in root and returns its teardown.
  function start(root) {
    // Listeners on the document, window and screen outlive the page's
    // markup, so they're kept to be removed by teardown.
    var listeners = [];
    var dead = false; // torn down: nothing may start playing any more
    function on(target, type, fn, opts) {
      target.addEventListener(type, fn, opts);
      listeners.push([target, type, fn, opts]);
    }
    var video = root.querySelector("video");
    var seek = root.querySelector(".player-seek");
    var played = root.querySelector(".player-played");
    var buffered = root.querySelector(".player-buffered");
    var thumb = root.querySelector(".player-thumb");
    var currentLabel = root.querySelector(".player-current");
    var totalLabel = root.querySelector(".player-total");
    var toggles = root.querySelectorAll('[data-action="toggle"]');
    var settingsBtn = root.querySelector('[data-action="settings"]');
    var menu = root.querySelector(".player-menu");
    var subList = menu.querySelector('[data-group="subtitles"] ul');
    var audioGroup = menu.querySelector('[data-group="audio"]');
    var audioList = audioGroup.querySelector("ul");
    var sizeGroup = menu.querySelector('[data-group="size"]');
    var sizeOut = sizeGroup.querySelector("output");
    var errorBox = root.querySelector(".player-error");

    var item = root.dataset.item;
    var series = root.dataset.series || "";
    var parts = JSON.parse(root.dataset.parts);
    var part = Number(root.dataset.part) || 0;
    var startAt = Number(root.dataset.start) || 0;
    var back = root.dataset.back || "/";
    var L = JSON.parse(root.dataset.labels);

    var session = null; // the /play answer for the current part
    // Subtitle size in percent of the default 1.4em, set with − and +.
    var subSize = Number(root.dataset.subSize) || 100;
    var SIZE_MIN = 50, SIZE_MAX = 250;
    var sizeSave = 0;
    var cueStyle = document.createElement("style");
    document.head.appendChild(cueStyle);
    var hls = null;
    var lastSave = 0;
    var leaving = false;

    // What this browser can play, for Jellyfin's device profile.
    var caps = (function () {
      var v = document.createElement("video");
      var ms = window.MediaSource;
      function ok(type) {
        return v.canPlayType(type) !== "" || (ms && ms.isTypeSupported && ms.isTypeSupported(type));
      }
      var containers = [];
      if (v.canPlayType("video/mp4")) containers.push("mp4", "m4v", "mov");
      if (v.canPlayType("video/webm")) containers.push("webm");
      var video = [], audio = [];
      var vc = { h264: 'video/mp4; codecs="avc1.640028"', hevc: 'video/mp4; codecs="hvc1.1.6.L120.90"',
        vp9: 'video/webm; codecs="vp9"', av1: 'video/mp4; codecs="av01.0.05M.08"' };
      var ac = { aac: 'audio/mp4; codecs="mp4a.40.2"', mp3: "audio/mpeg", opus: 'audio/webm; codecs="opus"',
        flac: "audio/flac", ac3: 'audio/mp4; codecs="ac-3"', eac3: 'audio/mp4; codecs="ec-3"' };
      Object.keys(vc).forEach(function (k) { if (ok(vc[k])) video.push(k); });
      Object.keys(ac).forEach(function (k) { if (ok(ac[k])) audio.push(k); });
      return { containers: containers, videoCodecs: video, audioCodecs: audio,
        hls: v.canPlayType("application/vnd.apple.mpegurl") !== "" };
    })();

    function clock(s) {
      s = Math.max(0, Math.floor(s || 0));
      var h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, sec = s % 60;
      var mm = h ? String(m).padStart(2, "0") : String(m);
      return (h ? h + ":" : "") + mm + ":" + String(sec).padStart(2, "0");
    }

    function duration() {
      var d = (session && session.duration) || parts[part].duration || video.duration || 0;
      return isFinite(d) ? d : 0; // a live stream's length is unknown
    }

    // icon points a button's <svg><use> at the #i-<name> symbol.
    function icon(button, name) {
      var use = button.querySelector("use");
      if (use) use.setAttribute("href", "#i-" + name);
    }

    function showError() {
      busy(false);
      root.classList.remove("loading");
      errorBox.textContent = L.error;
      errorBox.hidden = false;
    }

    // load asks /play for the current part. opts may hold audio and sub
    // (null = none); without them the person's languages decide.
    function load(opts) {
      var body = { caps: caps, start: opts.start || 0 };
      if ("audio" in opts) body.audio = opts.audio;
      if ("sub" in opts) body.sub = opts.sub;
      busy(true);
      root.classList.add("loading");
      return fetch("/play/" + parts[part].id, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
        credentials: "same-origin",
      }).then(function (r) {
        if (!r.ok) throw new Error("play " + r.status);
        return r.json();
      }).then(function (s) {
        session = s;
        if (dead) {
          save("stop"); // the page went while this stream was being set up
          return;
        }
        errorBox.hidden = true;
        attach(s, body.start);
        fillTracks(s);
      }).catch(showError);
    }

    function attach(s, start) {
      if (dead) return;
      if (hls) { hls.destroy(); hls = null; }
      Array.prototype.slice.call(video.querySelectorAll("track")).forEach(function (t) { t.remove(); });
      var seekTo = function () {
        if (start > 0 && Math.abs(video.currentTime - start) > 1) video.currentTime = start;
        // A refused autoplay (iOS) leaves the video paused: stop the spinner
        // so the play button shows.
        video.play().catch(function (err) {
          if (err && err.name === "AbortError") return; // a newer load took over
          busy(false);
          root.classList.remove("loading");
        });
      };
      video.addEventListener("loadedmetadata", seekTo, { once: true });
      if (s.direct || caps.hls) {
        video.src = s.url;
      } else if (window.Hls && window.Hls.isSupported()) {
        hls = new window.Hls({ startPosition: start || -1 });
        hls.on(window.Hls.Events.ERROR, function (_, data) { if (data.fatal) showError(); });
        hls.loadSource(s.url);
        hls.attachMedia(video);
      } else {
        showError();
        return;
      }
      // Text subtitles as tracks; the chosen one shows.
      s.subtitles.forEach(function (t) {
        if (!t.url) return;
        var el = document.createElement("track");
        el.kind = "subtitles";
        el.label = t.label;
        el.srclang = (t.lang || "").slice(0, 2);
        el.src = t.url;
        el.dataset.index = t.index;
        video.appendChild(el);
      });
      showSubtitle(s.sub);
    }

    function showSubtitle(index) {
      for (var i = 0; i < video.textTracks.length; i++) {
        var tt = video.textTracks[i];
        var el = video.querySelectorAll("track")[i];
        tt.mode = el && Number(el.dataset.index) === index ? "showing" : "disabled";
      }
    }

    // menuItem is one choice in the settings menu; value is "off" or a track index.
    function menuItem(value, label, checked) {
      var li = document.createElement("li");
      li.setAttribute("role", "none");
      var b = document.createElement("button");
      b.type = "button";
      b.setAttribute("role", "menuitemradio");
      b.setAttribute("aria-checked", checked ? "true" : "false");
      b.dataset.track = value;
      b.textContent = label;
      li.appendChild(b);
      return li;
    }

    function fillTracks(s) {
      subList.replaceChildren(menuItem("off", L.off, s.sub === null));
      s.subtitles.forEach(function (t) { subList.appendChild(menuItem(String(t.index), t.label || t.lang, s.sub === t.index)); });
      audioList.replaceChildren();
      s.audioTracks.forEach(function (t, i) {
        var chosen = s.audio === null ? i === 0 : s.audio === t.index;
        audioList.appendChild(menuItem(String(t.index), t.label || t.lang, chosen));
      });
      audioGroup.hidden = s.audioTracks.length < 2;
      // Picture subtitles are part of the picture: no size to change.
      var shown = s.sub === null ? null : track(s.subtitles, s.sub);
      sizeGroup.hidden = !shown || shown.image;
    }

    // applySize shows subtitles at n percent; ::cue gets a rule of its own
    // since it doesn't read custom properties in every browser.
    function applySize(n) {
      subSize = Math.min(SIZE_MAX, Math.max(SIZE_MIN, n));
      cueStyle.textContent = "#player video::cue { font-size: " + (1.4 * subSize / 100).toFixed(2) + "em; }";
      sizeOut.textContent = subSize + "%";
      sizeGroup.querySelectorAll("button[data-size]").forEach(function (b) {
        var end = Number(b.dataset.size) < 0 ? subSize <= SIZE_MIN : subSize >= SIZE_MAX;
        b.setAttribute("aria-disabled", end ? "true" : "false");
      });
    }
    applySize(subSize);
    // saveSize sends a size still waiting to be saved.
    function saveSize() {
      if (!sizeSave) return;
      clearTimeout(sizeSave);
      sizeSave = 0;
      savePrefs("subtitle_size", String(subSize));
    }

    function track(list, index) {
      for (var i = 0; i < list.length; i++) if (list[i].index === index) return list[i];
      return null;
    }

    function savePrefs(field, value) {
      var f = new FormData();
      f.append(field, value);
      fetch("/prefs", { method: "POST", body: f, credentials: "same-origin", keepalive: true }).catch(function () {});
    }

    function chooseSubtitle(index) {
      var t = index === null ? null : track(session.subtitles, index);
      savePrefs("subtitle_lang", t ? t.lang : "off");
      var wasImage = session.sub !== null && (track(session.subtitles, session.sub) || {}).image;
      if ((t && t.image) || wasImage) {
        // Picture subtitles are burned in: a new stream from here.
        load({ audio: session.audio, sub: index, start: video.currentTime });
      } else {
        session.sub = index;
        showSubtitle(index);
        fillTracks(session);
      }
    }

    function chooseAudio(index) {
      var t = track(session.audioTracks, index);
      if (t) savePrefs("audio_lang", t.lang);
      var sub = session.sub !== null && (track(session.subtitles, session.sub) || {}).image ? session.sub : null;
      load({ audio: index, sub: sub, start: video.currentTime });
    }

    function menuOpen() { return !menu.hidden; }
    function menuButtons() {
      return Array.prototype.filter.call(menu.querySelectorAll("button"), function (b) { return !b.closest("[hidden]"); });
    }
    function openMenu() {
      // Sit just above the controls, however tall the safe area makes them.
      var controls = root.querySelector(".player-controls").getBoundingClientRect();
      var bottom = Math.round(root.getBoundingClientRect().bottom - controls.top + 4);
      menu.style.bottom = bottom + "px";
      // Fit inside the player box, scrolling when it's short (inline on a phone).
      menu.style.maxHeight = Math.max(0, root.clientHeight - bottom - 8) + "px";
      menu.hidden = false;
      settingsBtn.setAttribute("aria-expanded", "true");
      root.classList.add("menu-open");
      var checked = menu.querySelector('button[aria-checked="true"]');
      (checked || menuButtons()[0] || settingsBtn).focus();
      wake();
    }
    function closeMenu() {
      if (menu.hidden) return;
      menu.hidden = true;
      settingsBtn.setAttribute("aria-expanded", "false");
      root.classList.remove("menu-open");
      wake();
    }

    menu.addEventListener("click", function (e) {
      var sb = e.target.closest("button[data-size]");
      if (sb) {
        // The menu stays open so the person can press again while watching.
        if (sb.getAttribute("aria-disabled") === "true") return;
        applySize(subSize + Number(sb.dataset.size));
        // One save after a run of presses: saves sent per press could
        // arrive out of order and keep an earlier size.
        clearTimeout(sizeSave);
        sizeSave = setTimeout(saveSize, 600);
        return;
      }
      var b = e.target.closest("button[data-track]");
      if (!b || !session) return;
      if (b.getAttribute("aria-checked") === "true") { closeMenu(); settingsBtn.focus(); return; } // already playing
      var v = b.dataset.track;
      if (b.closest('[data-group="subtitles"]')) chooseSubtitle(v === "off" ? null : Number(v));
      else chooseAudio(Number(v));
      closeMenu();
      settingsBtn.focus();
    });

    // save reports the place; event "stop" ends the Jellyfin session.
    function save(event) {
      if (!session) return;
      var f = new FormData();
      f.append("item", item);
      f.append("partItem", parts[part].id);
      f.append("series", series);
      f.append("part", String(part));
      f.append("position", String(Math.floor(video.currentTime || 0)));
      f.append("durations", parts.map(function (p, i) {
        return i === part && duration() ? duration() : p.duration || 0;
      }).join(","));
      f.append("playSessionId", session.playSessionId);
      f.append("mediaSourceId", session.mediaSourceId);
      f.append("event", event);
      if (event === "stop" && navigator.sendBeacon) {
        navigator.sendBeacon("/progress", f);
      } else {
        fetch("/progress", { method: "POST", body: f, credentials: "same-origin", keepalive: true }).catch(function () {});
      }
      lastSave = Date.now();
    }

    // setSeekUI moves the played fill and thumb to t seconds.
    function setSeekUI(t) {
      var d = duration();
      var pct = d > 0 ? Math.min(100, Math.max(0, (t / d) * 100)) : 0;
      played.style.width = pct + "%";
      thumb.style.left = pct + "%";
      seek.setAttribute("aria-valuemax", String(Math.floor(d)));
      seek.setAttribute("aria-valuenow", String(Math.floor(t || 0)));
      seek.setAttribute("aria-valuetext", clock(t) + " / " + clock(d));
    }

    function showBuffered() {
      var d = duration(), b = video.buffered, t = video.currentTime;
      var end = 0;
      for (var i = 0; i < b.length; i++) if (b.start(i) <= t + 0.5 && b.end(i) >= t) end = b.end(i);
      buffered.style.width = d > 0 ? Math.min(100, (end / d) * 100) + "%" : "0";
    }

    // Dragging the seek bar moves only the thumb and a time bubble; the
    // video seeks once, on release.
    var bubble = seek.querySelector(".player-bubble");
    var dragging = false, dragTime = 0;
    function timeAt(clientX) {
      var r = seek.getBoundingClientRect();
      var f = r.width > 0 ? Math.min(1, Math.max(0, (clientX - r.left) / r.width)) : 0;
      return f * duration();
    }
    function dragTo(clientX) {
      dragTime = timeAt(clientX);
      setSeekUI(dragTime);
      var d = duration();
      bubble.textContent = clock(dragTime);
      bubble.hidden = false;
      var w = seek.clientWidth, half = bubble.offsetWidth / 2;
      var x = d > 0 ? (dragTime / d) * w : 0;
      bubble.style.left = Math.max(half, Math.min(w - half, x)) + "px";
      currentLabel.textContent = clock(dragTime);
    }
    seek.addEventListener("pointerdown", function (e) {
      if (e.button !== 0) return;
      dragging = true;
      root.classList.add("seeking");
      try { seek.setPointerCapture(e.pointerId); } catch (_) {}
      dragTo(e.clientX);
      e.preventDefault();
    });
    seek.addEventListener("pointermove", function (e) { if (dragging) dragTo(e.clientX); });
    function endDrag(commit) {
      if (!dragging) return;
      dragging = false;
      root.classList.remove("seeking");
      bubble.hidden = true;
      if (commit && duration() > 0) video.currentTime = dragTime;
      showTime();
      wake();
    }
    seek.addEventListener("pointerup", function () { endDrag(true); });
    seek.addEventListener("pointercancel", function () { endDrag(false); });
    seek.addEventListener("keydown", function (e) {
      if (e.key === "Home") { e.preventDefault(); video.currentTime = 0; }
      else if (e.key === "End") { e.preventDefault(); video.currentTime = Math.max(0, duration() - 1); }
      else if (e.key === "PageUp") { e.preventDefault(); jump(60); }
      else if (e.key === "PageDown") { e.preventDefault(); jump(-60); }
    });
    video.addEventListener("progress", showBuffered);

    function showTime() {
      showBuffered();
      if (dragging) return;
      setSeekUI(video.currentTime);
      currentLabel.textContent = clock(video.currentTime);
      totalLabel.textContent = clock(duration());
    }
    video.addEventListener("loadedmetadata", showTime);
    // The box takes the video's own shape, so a film wider than 16:9 isn't
    // letterboxed inside it (a band above the picture, and one below under
    // the controls). Kept between 4:3 and 2.76:1.
    video.addEventListener("loadedmetadata", function () {
      if (!video.videoWidth || !video.videoHeight) return;
      var ratio = Math.min(2.76, Math.max(4 / 3, video.videoWidth / video.videoHeight));
      root.style.setProperty("--video-ratio", ratio.toFixed(4));
    });
    video.addEventListener("timeupdate", function () {
      showTime();
      if (!video.paused && Date.now() - lastSave >= 10000) save("progress");
    });
    video.addEventListener("play", function () {
      Array.prototype.forEach.call(toggles, function (b) { icon(b, "pause"); b.setAttribute("aria-label", L.pause); });
    });
    video.addEventListener("pause", function () {
      Array.prototype.forEach.call(toggles, function (b) { icon(b, "play"); b.setAttribute("aria-label", L.play); });
      if (!leaving && !video.ended) save("progress");
    });
    video.addEventListener("ended", function () {
      if (dead) return;
      save("stop");
      if (part + 1 < parts.length) {
        part++;
        load({ start: 0 });
      } else {
        leaving = true;
        location.href = back;
      }
    });

    function toggle() { if (video.paused) video.play().catch(function () {}); else video.pause(); }
    function jump(s) {
      var d = duration(), t = video.currentTime + s;
      video.currentTime = Math.max(0, d > 0 ? Math.min(d, t) : t); // unknown length: no end to stop at
    }
    // Fullscreen: the whole player where the browser allows it; on iPhone
    // only the video can go fullscreen, with the system's own controls.
    // Phones turn to landscape where the browser lets a page ask for it.
    var fsButton = root.querySelector('[data-action="fullscreen"]');
    function fsElement() { return document.fullscreenElement || document.webkitFullscreenElement || null; }
    function isFull() { return !!fsElement() || !!video.webkitDisplayingFullscreen; }
    function noop() {}
    function lockLandscape() {
      try {
        if (screen.orientation && screen.orientation.lock) screen.orientation.lock("landscape").catch(noop);
      } catch (_) {}
    }
    function unlockOrientation() {
      try { if (screen.orientation && screen.orientation.unlock) screen.orientation.unlock(); } catch (_) {}
    }
    function fullscreen() {
      if (fsElement()) {
        if (document.exitFullscreen) document.exitFullscreen().catch(noop);
        else if (document.webkitExitFullscreen) document.webkitExitFullscreen();
        return;
      }
      // iPhone may have the element methods but with fullscreen disabled.
      var enabled = "fullscreenEnabled" in document ? document.fullscreenEnabled : document.webkitFullscreenEnabled;
      if (enabled && root.requestFullscreen) root.requestFullscreen().then(lockLandscape, noop);
      else if (enabled && root.webkitRequestFullscreen) { root.webkitRequestFullscreen(); lockLandscape(); }
      else videoFullscreen();
    }
    function videoFullscreen() {
      if (video.webkitEnterFullscreen) { try { video.webkitEnterFullscreen(); } catch (_) {} }
    }
    function showFullscreen() {
      var full = isFull();
      icon(fsButton, full ? "exit-fullscreen" : "fullscreen");
      fsButton.setAttribute("aria-label", full ? L.exitFullscreen : L.fullscreen);
      if (!full) unlockOrientation();
    }
    on(document, "fullscreenchange", showFullscreen);
    on(document, "webkitfullscreenchange", showFullscreen);
    video.addEventListener("webkitbeginfullscreen", showFullscreen);
    video.addEventListener("webkitendfullscreen", showFullscreen);
    fsButton.hidden = !(root.requestFullscreen || root.webkitRequestFullscreen || video.webkitEnterFullscreen);

    // Mute and volume, remembered in this browser.
    var muteBtn = root.querySelector('[data-action="mute"]');
    var volumeInput = root.querySelector(".player-volume");
    var VOLUME_KEY = "media.player.volume";
    try {
      var saved = JSON.parse(localStorage.getItem(VOLUME_KEY) || "null");
      if (saved && typeof saved.volume === "number") video.volume = Math.min(1, Math.max(0, saved.volume));
      if (saved && saved.muted) video.muted = true;
    } catch (_) {}
    function showVolume() {
      var silent = video.muted || video.volume === 0;
      icon(muteBtn, silent ? "muted" : "volume");
      muteBtn.setAttribute("aria-label", silent ? L.unmute : L.mute);
      volumeInput.value = String(silent ? 0 : video.volume);
    }
    function toggleMute() {
      if (video.muted || video.volume === 0) {
        if (video.volume === 0) video.volume = 0.5;
        video.muted = false;
      } else {
        video.muted = true;
      }
    }
    volumeInput.addEventListener("input", function () {
      var v = Number(volumeInput.value);
      video.volume = v;
      video.muted = v === 0;
    });
    video.addEventListener("volumechange", function () {
      showVolume();
      try { localStorage.setItem(VOLUME_KEY, JSON.stringify({ muted: video.muted, volume: video.volume })); } catch (_) {}
    });
    showVolume();

    // busy shows the spinner while the video waits for data.
    var spinner = root.querySelector(".player-spinner");
    function busy(on) {
      spinner.hidden = !on;
      root.classList.toggle("busy", on);
    }
    // Only a playing video can be waiting for data; a paused one just shows play.
    video.addEventListener("waiting", function () { if (!video.paused) busy(true); });
    video.addEventListener("stalled", function () { if (!video.paused && video.readyState < 3) busy(true); });
    video.addEventListener("play", function () { root.classList.add("is-playing"); });
    video.addEventListener("pause", function () { root.classList.remove("is-playing"); });
    ["playing", "canplay", "pause", "error"].forEach(function (ev) { video.addEventListener(ev, function () { busy(false); }); });

    root.addEventListener("click", function (e) {
      if (menuOpen() && !e.target.closest(".player-menu") && !e.target.closest('[data-action="settings"]')) {
        closeMenu();
        if (!e.target.closest("[data-action]")) return; // a tap beside the menu only closes it
      }
      var a = e.target.closest("[data-action]");
      if (!a) return; // taps on the picture are handled in the gestures below
      switch (a.dataset.action) {
        case "toggle": toggle(); break;
        case "back10": jump(-10); break;
        case "forward10": jump(10); break;
        case "fullscreen": fullscreen(); break;
        case "settings": if (menuOpen()) closeMenu(); else openMenu(); break;
        case "mute": toggleMute(); break;
      }
    });

    function keyboardFocused(el) {
      try { return el.matches(":focus-visible"); } catch (_) { return false; } // older Safari
    }

    // Up and Down walk between the picture, the seek bar and the buttons
    // (a remote has no mouse and no Tab), and leave the player at either
    // end: Down from the buttons to what follows it (the episode list), Up
    // from the picture to what precedes it (the header). spatial-nav takes
    // over from there; it leaves keys alone while focus is in the player.
    // Left and Right walk along the buttons; on the picture and the seek
    // bar they still skip.
    var PRESSABLE = "a[href], button, summary, input, select, textarea, [tabindex]";
    function pressable(el) {
      if (root.contains(el) || el.disabled || el.tabIndex < 0 || el.closest("[hidden], [inert]")) return false;
      var r = el.getBoundingClientRect();
      if (r.width <= 0 || r.height <= 0) return false;
      var cs = getComputedStyle(el);
      return cs.visibility !== "hidden" && cs.display !== "none";
    }
    // outside picks the nearest pressable element after (or before) the
    // player in the page's order.
    function outside(after) {
      var side = after ? Node.DOCUMENT_POSITION_FOLLOWING : Node.DOCUMENT_POSITION_PRECEDING;
      var list = Array.prototype.filter.call(document.querySelectorAll(PRESSABLE), function (el) {
        return (root.compareDocumentPosition(el) & side) && pressable(el);
      });
      return after ? list[0] : list[list.length - 1];
    }
    function leave(el) {
      // Fullscreen only shows the player; the rest of the page is still
      // there, focusable but invisible, and OK on it would navigate away
      // mid-video. So focus stays inside the player.
      if (!el || fsElement()) return; // nothing that way (or fullscreen): stay
      el.focus({ preventScroll: true });
      el.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
    function barStops() {
      return Array.prototype.filter.call(root.querySelectorAll(".player-bar button, .player-bar input"), function (el) {
        var r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0; // the narrow layout hides some
      });
    }
    function inControls(el) { return !!el.closest(".player-controls"); }
    function inBar(el) { return !!el.closest(".player-bar"); }
    function focusKeyboard(el) { if (el) el.focus({ preventScroll: true }); }
    function moveVertical(key) {
      var f = document.activeElement;
      if (key === "ArrowDown") {
        if (!inControls(f)) focusKeyboard(seek);
        else if (f === seek) focusKeyboard(root.querySelector('.player-bar [data-action="toggle"]') || barStops()[0]);
        else if (inBar(f)) leave(outside(true));
      } else {
        if (inBar(f)) focusKeyboard(seek);
        else if (inControls(f) || f.closest(".player-center")) focusKeyboard(root);
        else if (f === root) leave(outside(false));
      }
    }
    function moveAlongBar(key, f) {
      var stops = barStops(), i = stops.indexOf(f), j = i + (key === "ArrowRight" ? 1 : -1);
      if (i >= 0 && stops[j]) focusKeyboard(stops[j]); // the ends stay put
    }

    on(document, "keydown", function (e) {
      if (e.ctrlKey || e.metaKey || e.altKey) return; // the browser's (Find, Back…)
      // An arrow someone else already handled (spatial-nav) isn't ours too.
      if (e.defaultPrevented && /^Arrow/.test(e.key)) return;
      // The remote's media keys work wherever focus is on the watch page.
      // Play and Pause are separate keys on some remotes, so they don't toggle.
      var mediaKey = true;
      switch (e.key) {
        case "MediaPlayPause": toggle(); break;
        case "MediaPlay": if (video.paused) toggle(); break;
        case "MediaPause": if (!video.paused) toggle(); break;
        case "MediaFastForward": jump(10); flash("right", 10); break;
        case "MediaRewind": jump(-10); flash("left", -10); break;
        default: mediaKey = false;
      }
      if (mediaKey) { e.preventDefault(); wake(); return; }
      if (e.target.closest && e.target.closest("input, textarea, select, [contenteditable]")) {
        // The volume slider keeps Left and Right for the volume until its
        // end; Up and Down are how focus leaves it.
        if (e.target.matches && e.target.matches(".player-volume") && (e.key === "ArrowUp" || e.key === "ArrowDown")) {
          e.preventDefault(); wake(); moveVertical(e.key); return;
        }
        // At the end of its range a Right (or Left) can't change the volume
        // any more, so it moves on to the next button: without this the
        // buttons after the slider (settings, fullscreen) are out of reach.
        // The saved volume starts at the maximum, so Right usually passes at once.
        var vol = e.target;
        if (vol.matches && vol.matches(".player-volume") &&
            ((e.key === "ArrowRight" && +vol.value >= +vol.max) || (e.key === "ArrowLeft" && +vol.value <= +vol.min))) {
          e.preventDefault(); wake(); moveAlongBar(e.key, vol); return;
        }
        if (e.key !== "Escape") return;
      }
      // The page's own keys stay the page's: Space and arrows act on the
      // player only when it has focus (else they scroll, press a link…);
      // the letters also when nothing in particular has focus.
      var focus = document.activeElement;
      var inPlayer = focus && root.contains(focus);
      var onPage = !focus || focus === document.body || focus === document.documentElement;
      if (!inPlayer && !(onPage && /^[kfmKFM]$/.test(e.key)) && !(e.key === "Escape" && menuOpen())) return;
      wake();
      if (e.key === "Escape" && menuOpen()) { e.preventDefault(); closeMenu(); settingsBtn.focus(); return; }
      if (menuOpen() && menu.contains(document.activeElement)) {
        // Up/Down/Home/End move through the menu; Enter and Space choose
        // natively; Tab leaves it closed.
        var bs = menuButtons(), i = bs.indexOf(document.activeElement), next = -1;
        if (e.key === "ArrowDown") next = (i + 1) % bs.length;
        else if (e.key === "ArrowUp") next = (i - 1 + bs.length) % bs.length;
        else if (e.key === "Home") next = 0;
        else if (e.key === "End") next = bs.length - 1;
        else if ((e.key === "ArrowLeft" || e.key === "ArrowRight") && document.activeElement.dataset.size) {
          // Between − and +, as they sit side by side.
          next = bs.indexOf(sizeGroup.querySelector('button[data-size="' + (e.key === "ArrowLeft" ? "-10" : "10") + '"]'));
        }
        else if (e.key === "Tab") { closeMenu(); settingsBtn.focus(); e.preventDefault(); return; }
        if (next >= 0 && bs[next]) { e.preventDefault(); bs[next].focus(); }
        return;
      }
      // Space on a button reached with the keyboard presses it; after a
      // mouse click it still plays or pauses.
      if (e.key === " " && e.target.closest && e.target.closest("button") && keyboardFocused(e.target)) return;
      // A remote's OK (Enter) on the picture plays or pauses, as a click on
      // it does; on a button it stays that button's own press.
      if (e.key === "Enter" && (focus === root || focus === video)) { e.preventDefault(); toggle(); return; }
      if (inPlayer && (e.key === "ArrowUp" || e.key === "ArrowDown")) { e.preventDefault(); moveVertical(e.key); return; }
      if (inPlayer && (e.key === "ArrowLeft" || e.key === "ArrowRight") && inBar(focus)) { e.preventDefault(); moveAlongBar(e.key, focus); return; }
      switch (e.key) {
        case " ": case "k": e.preventDefault(); toggle(); break;
        case "ArrowLeft": e.preventDefault(); jump(-10); break;
        case "ArrowRight": e.preventDefault(); jump(10); break;
        case "f": case "F": e.preventDefault(); fullscreen(); break;
        case "m": case "M": e.preventDefault(); toggleMute(); break;
      }
    });

    // Controls hide after 3 seconds without input while playing, but not
    // while the menu is open or the seek bar is held.
    var idleTimer = null;
    function wake() {
      root.classList.remove("idle");
      clearTimeout(idleTimer);
      idleTimer = setTimeout(function () {
        if (!video.paused && !menuOpen() && !dragging) root.classList.add("idle");
      }, 3000);
    }
    function showControls() { wake(); }
    function hideControls() { clearTimeout(idleTimer); root.classList.add("idle"); }
    video.addEventListener("pause", function () { root.classList.remove("idle"); });
    // Start the hide timer when playback starts late, but don't bring
    // hidden controls back after a stall or a skip.
    video.addEventListener("playing", function () {
      root.classList.remove("loading");
      if (!root.classList.contains("idle")) wake();
    });
    // The menu's place depends on the controls' height; close it when the window changes.
    on(window, "resize", closeMenu);

    // flash shows "−10" / "+10" on one side; quick repeats add up.
    var flashes = { left: { el: root.querySelector(".player-flash.left"), sum: 0, timer: null },
      right: { el: root.querySelector(".player-flash.right"), sum: 0, timer: null } };
    function flash(side, seconds) {
      var f = flashes[side];
      f.sum += seconds;
      f.el.textContent = (f.sum < 0 ? "−" : "+") + Math.abs(f.sum);
      f.el.hidden = false;
      clearTimeout(f.timer);
      f.timer = setTimeout(function () { f.el.hidden = true; f.sum = 0; }, 600);
    }

    // Taps on the picture (not on a control). Touch: one tap shows or
    // hides the controls; two taps skip on the left or right third and
    // toggle fullscreen in the middle. Mouse: click plays or pauses,
    // double click toggles fullscreen.
    var TAP_MS = 250;
    var tapTimer = null, lastTapAt = 0, lastDoubleAt = 0, lastTouchAt = 0;
    function onSurface(el) {
      return !el.closest("button, a, input, .player-menu, .player-controls");
    }
    function sideOf(clientX) {
      var r = root.getBoundingClientRect();
      var f = (clientX - r.left) / (r.width || 1);
      return f < 1 / 3 ? "left" : f > 2 / 3 ? "right" : "middle";
    }
    function doubleTap(side) {
      if (side === "left") { jump(-10); flash("left", -10); }
      else if (side === "right") { jump(10); flash("right", 10); }
      else fullscreen();
    }
    root.addEventListener("pointerdown", function (e) {
      var touch = e.pointerType !== "mouse"; // touch and pen
      if (touch) lastTouchAt = Date.now();
      // Touching a control keeps the controls up; a tap on the picture
      // decides for itself once it is known to be single or double.
      if (!touch || !onSurface(e.target)) wake();
    }, { passive: true });
    root.addEventListener("mousemove", function () {
      if (Date.now() - lastTouchAt > 800) wake(); // browsers fake a mousemove after a tap
    }, { passive: true });
    root.addEventListener("pointerup", function (e) {
      if (e.button !== 0 || !onSurface(e.target) || menuOpen()) return;
      var now = Date.now();
      var touch = e.pointerType !== "mouse";
      var side = sideOf(e.clientX);
      // Further quick taps after a double tap keep skipping.
      if (touch && side !== "middle" && now - lastDoubleAt < 600) {
        clearTimeout(tapTimer); tapTimer = null;
        lastDoubleAt = now;
        doubleTap(side);
        return;
      }
      if (tapTimer && now - lastTapAt < TAP_MS) {
        clearTimeout(tapTimer); tapTimer = null;
        lastDoubleAt = touch && side !== "middle" ? now : 0; // only side skips chain
        if (touch) doubleTap(side); else fullscreen();
        return;
      }
      lastTapAt = now;
      tapTimer = setTimeout(function () {
        tapTimer = null;
        if (!touch) { toggle(); wake(); return; }
        if (root.classList.contains("idle")) showControls(); else hideControls();
      }, TAP_MS);
    });

    on(window, "pagehide", function () { saveSize(); if (!leaving) { leaving = true; save("stop"); } });

    root.focus({ preventScroll: true });
    wake();
    load({ start: startAt });

    return function teardown() {
      dead = true;
      saveSize();
      cueStyle.remove();
      if (!leaving) {
        leaving = true;
        save("stop");
      }
      listeners.forEach(function (l) { l[0].removeEventListener(l[1], l[2], l[3]); });
      clearTimeout(idleTimer);
      clearTimeout(tapTimer);
      clearTimeout(flashes.left.timer);
      clearTimeout(flashes.right.timer);
      if (hls) { hls.destroy(); hls = null; }
      video.pause();
      video.removeAttribute("src");
      video.load();
    };
  }
})();
