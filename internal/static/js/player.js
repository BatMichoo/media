// The media app's player: asks the app how to play each part in this
// browser (/play), plays it directly or as HLS (hls.js where needed),
// saves the place every 10 seconds (/progress), and remembers subtitle
// and audio languages (/prefs).
(function () {
  "use strict";
  var root = document.getElementById("player");
  if (!root) return;
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
  var errorBox = root.querySelector(".player-error");

  var item = root.dataset.item;
  var series = root.dataset.series || "";
  var parts = JSON.parse(root.dataset.parts);
  var part = Number(root.dataset.part) || 0;
  var startAt = Number(root.dataset.start) || 0;
  var back = root.dataset.back || "/";
  var L = JSON.parse(root.dataset.labels);

  var session = null; // the /play answer for the current part
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
    return (session && session.duration) || parts[part].duration || video.duration || 0;
  }

  // icon points a button's <svg><use> at the #i-<name> symbol.
  function icon(button, name) {
    var use = button.querySelector("use");
    if (use) use.setAttribute("href", "#i-" + name);
  }

  function showError() {
    busy(false);
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
      errorBox.hidden = true;
      attach(s, body.start);
      fillTracks(s);
    }).catch(showError);
  }

  function attach(s, start) {
    if (hls) { hls.destroy(); hls = null; }
    Array.prototype.slice.call(video.querySelectorAll("track")).forEach(function (t) { t.remove(); });
    var seekTo = function () {
      if (start > 0 && Math.abs(video.currentTime - start) > 1) video.currentTime = start;
      video.play().catch(function () {});
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
  }

  function track(list, index) {
    for (var i = 0; i < list.length; i++) if (list[i].index === index) return list[i];
    return null;
  }

  function savePrefs(field, value) {
    var f = new FormData();
    f.append(field, value);
    fetch("/prefs", { method: "POST", body: f, credentials: "same-origin" }).catch(function () {});
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
  function openMenu() {
    menu.hidden = false;
    settingsBtn.setAttribute("aria-expanded", "true");
    root.classList.add("menu-open");
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
    var b = e.target.closest("button[data-track]");
    if (!b || !session) return;
    var v = b.dataset.track;
    if (b.closest('[data-group="subtitles"]')) chooseSubtitle(v === "off" ? null : Number(v));
    else if (Number(v) !== session.audio) chooseAudio(Number(v));
    closeMenu();
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
    bubble.style.left = (d > 0 ? (dragTime / d) * 100 : 0) + "%";
    bubble.hidden = false;
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
  function jump(s) { video.currentTime = Math.max(0, Math.min(duration(), video.currentTime + s)); }
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
    if (root.requestFullscreen) root.requestFullscreen().then(lockLandscape, noop);
    else if (root.webkitRequestFullscreen) { root.webkitRequestFullscreen(); lockLandscape(); }
    else if (video.webkitEnterFullscreen) { try { video.webkitEnterFullscreen(); } catch (_) {} }
  }
  function showFullscreen() {
    var full = isFull();
    icon(fsButton, full ? "exit-fullscreen" : "fullscreen");
    fsButton.setAttribute("aria-label", full ? L.exitFullscreen : L.fullscreen);
    if (!full) unlockOrientation();
  }
  document.addEventListener("fullscreenchange", showFullscreen);
  document.addEventListener("webkitfullscreenchange", showFullscreen);
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
  video.addEventListener("waiting", function () { busy(true); });
  video.addEventListener("stalled", function () { if (video.readyState < 3) busy(true); });
  ["playing", "canplay", "pause", "error"].forEach(function (ev) { video.addEventListener(ev, function () { busy(false); }); });

  root.addEventListener("click", function (e) {
    if (menuOpen() && !e.target.closest(".player-menu") && !e.target.closest('[data-action="settings"]')) {
      closeMenu();
      return;
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
      case "back": e.preventDefault(); leave(); break;
    }
  });

  function leave() {
    leaving = true;
    save("stop");
    location.href = back;
  }

  document.addEventListener("keydown", function (e) {
    if (e.target.tagName === "INPUT") {
      if (e.key !== "Escape") return;
    }
    wake();
    if (e.key === "Escape" && menuOpen()) { e.preventDefault(); closeMenu(); settingsBtn.focus(); return; }
    if (menuOpen()) return; // arrows and space belong to the menu
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
    return !el.closest("button, a, input, .player-menu, .player-controls, .player-top");
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
    if (e.pointerType === "touch") lastTouchAt = Date.now();
    // Touching a control keeps the controls up; a tap on the picture
    // decides for itself once it is known to be single or double.
    if (e.pointerType !== "touch" || !onSurface(e.target)) wake();
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
      lastDoubleAt = now;
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

  window.addEventListener("pagehide", function () { if (!leaving) { leaving = true; save("stop"); } });

  root.focus();
  wake();
  load({ start: startAt });
})();
