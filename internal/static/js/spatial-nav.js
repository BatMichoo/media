// Arrow-key navigation for the browsing pages: a TV remote's D-pad (and a
// keyboard) moves focus to the nearest link or button in the pressed
// direction, so every card and button can be reached without a mouse or Tab.
//
// It stays out of the way of keys that already mean something: a text
// field keeps Left/Right (caret), a select Up/Down, a slider Left/Right,
// and the player owns every arrow inside it.
(function () {
  "use strict";
  // htmx runs this file again each time it swaps a page in; the listener is
  // on the document and outlives every swap, so one copy is enough.
  if (window.mediaSpatialNav) return;
  window.mediaSpatialNav = true;

  var FOCUSABLE = 'a[href], button, summary, input, select, textarea, [tabindex]';
  var DIRECTIONS = { ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down" };

  // reachable: on screen as something a person could see and press. Hidden,
  // disabled or zero-sized elements (a closed <details>' form, the narrow
  // layout's dropped buttons) must not catch focus.
  function reachable(el) {
    if (el.disabled || el.tabIndex < 0 || el.closest("[hidden], [inert]")) return false;
    if (el.tagName === "INPUT" && el.type === "hidden") return false;
    // A closed <details> (the header's Settings menu) hides its content but
    // newer browsers still give it a size and place, so without this the
    // nearest pick can be a button that can't take focus, and focus stays
    // stuck. Its own <summary> stays reachable.
    var closed = el.closest("details:not([open])");
    if (closed && !(el.tagName === "SUMMARY" && el.parentElement === closed)) return false;
    if (el.checkVisibility && !el.checkVisibility()) return false;
    var r = el.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return false;
    var cs = getComputedStyle(el);
    return cs.visibility !== "hidden" && cs.display !== "none";
  }

  var TEXT_INPUT = /^(text|search|email|number|password|url|tel)$/;

  // usesKey: the focused field needs this arrow for itself. Text fields use
  // Left and Right (caret), a select uses Up and Down (options); everything
  // else (file, checkbox, button inputs, links) hands every arrow to us, or
  // focus would be trapped in the subtitle upload form.
  function usesKey(el, dir) {
    var horizontal = dir === "left" || dir === "right";
    if (el.isContentEditable) return true;
    if (el.tagName === "TEXTAREA") return true;
    if (el.tagName === "SELECT") return !horizontal;
    if (el.tagName === "INPUT") {
      if (el.type === "range") return horizontal;
      return TEXT_INPUT.test(el.type) && horizontal;
    }
    return false;
  }

  function candidates() {
    return Array.prototype.filter.call(document.querySelectorAll(FOCUSABLE), reachable);
  }

  // best picks the candidate nearest to `from` in `dir`. The pages are rows
  // of cards: Up and Down go to the nearest row first (sideways distance
  // counts half), Left and Right stay in the same row when anything there
  // lies that way (sideways drift counts twice, and leaving the row costs
  // more than any distance on screen).
  function best(from, list, dir) {
    var a = from.getBoundingClientRect();
    var ax = a.left + a.width / 2, ay = a.top + a.height / 2;
    var pick = null, pickScore = Infinity;
    list.forEach(function (el) {
      if (el === from || from.contains(el) || el.contains(from)) return;
      var b = el.getBoundingClientRect();
      var bx = b.left + b.width / 2, by = b.top + b.height / 2;
      var along, across;
      if (dir === "right") { if (bx <= ax) return; along = Math.max(0, b.left - a.right); across = Math.abs(by - ay); }
      else if (dir === "left") { if (bx >= ax) return; along = Math.max(0, a.left - b.right); across = Math.abs(by - ay); }
      else if (dir === "down") { if (by <= ay) return; along = Math.max(0, b.top - a.bottom); across = Math.abs(bx - ax); }
      else { if (by >= ay) return; along = Math.max(0, a.top - b.bottom); across = Math.abs(bx - ax); }
      var score;
      if (dir === "left" || dir === "right") {
        var sameRow = b.top < a.bottom && b.bottom > a.top;
        score = along + 2 * across + (sameRow ? 0 : 1e6);
      } else {
        score = along + across / 2;
      }
      if (score < pickScore) { pick = el; pickScore = score; }
    });
    return pick;
  }

  // first is where focus lands when nothing has it yet: the page's own
  // content before the header, topmost then leftmost among what is on screen.
  function first(list) {
    var inView = list.filter(function (el) {
      var r = el.getBoundingClientRect();
      return r.bottom > 0 && r.top < window.innerHeight;
    });
    var main = inView.filter(function (el) { return el.closest("main"); });
    var pool = main.length ? main : inView.length ? inView : list;
    pool.sort(function (x, y) {
      var a = x.getBoundingClientRect(), b = y.getBoundingClientRect();
      return a.top - b.top || a.left - b.left;
    });
    return pool[0] || null;
  }

  function focus(el) {
    el.focus({ preventScroll: true });
    // "nearest" scrolls only as far as needed, in both axes (a row of cards
    // scrolls sideways, the page up or down).
    el.scrollIntoView({ block: "nearest", inline: "nearest" });
  }

  document.addEventListener("keydown", function (e) {
    var dir = DIRECTIONS[e.key];
    if (!dir || e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) return;
    // While anything is fullscreen (the player), the rest of the page is
    // hidden, so moving focus to it would put it out of sight.
    if (document.fullscreenElement || document.webkitFullscreenElement) return;
    var f = document.activeElement;
    if (f && f.closest && f.closest(".player")) return;
    if (f && usesKey(f, dir)) return;
    var list = candidates();
    var onPage = !f || f === document.body || f === document.documentElement;
    var next = onPage ? first(list) : best(f, list, dir);
    // Nothing further that way: leave the key alone so the page can scroll.
    if (!next) return;
    e.preventDefault();
    focus(next);
  });
})();
