// Himay web: keyboard navigation, client-side folding, tabs. No eval, no inline handlers.
(function () {
  "use strict";
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));

  // ---- detail: inline pane on wide screens, modal dialog below ----
  const wide = window.matchMedia("(min-width: 96rem)"); // Tailwind 2xl
  function pane() { return $("#detail-pane"); }
  function syncPane() {
    const d = pane();
    if (!d) return;
    if (wide.matches) {
      if (d.matches(":modal")) d.close();
      if (!d.open) d.show(); // non-modal: sits in the grid as a column
    } else if (d.open && !d.matches(":modal")) {
      d.close();
    }
  }
  function openModal() {
    const d = pane();
    if (!d || wide.matches || d.matches(":modal")) return;
    if (d.open) d.close();
    d.showModal();
    $("#detail", d).focus({ preventScroll: true });
  }
  function closeModal() {
    const d = pane();
    if (d && d.matches(":modal")) { d.close(); const r = current(); if (r) r.focus({ preventScroll: true }); }
  }
  wide.addEventListener("change", syncPane);
  document.addEventListener("DOMContentLoaded", syncPane);

  // ---- trace selection ----
  function rows() { return $$("#rows .row").filter(r => !r.hidden); }
  function current() { return $("#rows .row[aria-selected='true']"); }
  function select(row, load = true) {
    if (!row) return;
    const prev = current();
    if (prev) prev.setAttribute("aria-selected", "false");
    row.setAttribute("aria-selected", "true");
    row.scrollIntoView({ block: "nearest" });
    if (load && window.htmx) htmx.trigger(row, "select");
  }
  function move(delta) {
    const list = rows();
    if (!list.length) return;
    const i = list.indexOf(current());
    select(list[Math.max(0, Math.min(list.length - 1, (i < 0 ? -1 : i) + delta))]);
  }

  // ---- folding (client-side: hide rows deeper than the step until the next step) ----
  function childrenOf(step) {
    const out = [];
    let el = step.nextElementSibling;
    while (el && el.classList.contains("row") && !el.hasAttribute("data-step") && !el.classList.contains("d0")) {
      out.push(el);
      el = el.nextElementSibling;
    }
    return out;
  }
  function setFold(step, folded) {
    if (!step || !step.hasAttribute("data-step")) return;
    const kids = childrenOf(step);
    if (!kids.length) return;
    kids.forEach(k => { k.hidden = folded; });
    const f = $(".fold", step);
    if (f) f.textContent = folded ? "▸" : "▾";
    step.dataset.folded = folded ? "1" : "";
  }
  function stepFor(row) {
    if (!row) return null;
    if (row.hasAttribute("data-step")) return row;
    let el = row.previousElementSibling;
    while (el && !el.hasAttribute("data-step")) el = el.previousElementSibling;
    return el;
  }

  document.addEventListener("click", e => {
    const fold = e.target.closest("#rows .row[data-step] .fold");
    if (fold) {
      const step = fold.closest(".row");
      setFold(step, step.dataset.folded !== "1");
      e.stopPropagation();
      return;
    }
    const row = e.target.closest("#rows .row");
    if (row && !e.target.closest("a")) { select(row, false); openModal(); } // htmx handles the load

    if (e.target.closest("[data-close-detail]")) { closeModal(); return; }
    // Click on the backdrop (the dialog element itself, outside its content) closes it.
    const d = pane();
    if (d && e.target === d && d.matches(":modal")) {
      const r = d.getBoundingClientRect();
      if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) closeModal();
    }

    const tab = e.target.closest("[data-tab]");
    if (tab) $$("[data-tab]").forEach(t => t.setAttribute("aria-selected", String(t === tab)));

    const kids = e.target.closest("[data-toggle-kids]");
    if (kids) {
      const ul = kids.closest("li").querySelector(".kids");
      const open = kids.getAttribute("aria-expanded") === "true";
      kids.setAttribute("aria-expanded", String(!open));
      kids.textContent = open ? "▸" : "▾";
      if (ul) ul.hidden = open;
      if (open || (ul && ul.children.length)) e.preventDefault(), e.stopImmediatePropagation();
    }

    const jump = e.target.closest("[data-jump]");
    if (jump) jumpTo(jump.dataset.jump);

    if (e.target.closest("[data-close-search]")) {
      $("#search-results").innerHTML = "";
      const q = $("#q"); if (q) q.value = "";
    }
  }, true);

  // Jump from Instructions/Files back into the trace and select a row.
  function jumpTo(idx) {
    const traceTab = $("[data-tab]");
    const go = () => {
      const row = $(`#rows .row[data-idx="${CSS.escape(idx)}"]`);
      if (row) { const st = stepFor(row); if (st && st !== row) setFold(st, false); select(row); }
    };
    if ($("#rows")) return go();
    if (traceTab) {
      document.body.addEventListener("htmx:afterSettle", function once() {
        document.body.removeEventListener("htmx:afterSettle", once);
        go();
      });
      traceTab.click();
    }
  }

  // ---- h/l: context-sensitive navigation (same rules as the TUI) ----
  function open(row) {
    if (!row) return;
    if (row.dataset.child) location.href = row.dataset.child;
    else if (window.htmx) { htmx.trigger(row, "select"); openModal(); }
  }
  function isFoldable(row) { return row && row.hasAttribute("data-step") && childrenOf(row).length > 0; }
  // l: a folded step expands, an open step steps into its first child, anything else opens.
  function right(row) {
    if (!row) return;
    if (isFoldable(row)) {
      if (row.dataset.folded === "1") setFold(row, false);
      else move(1);
      return;
    }
    open(row);
  }
  // h: in detail, close it; an open step folds; a row inside a step folds it and
  // moves there; a top-level row goes back. A folded step stays put.
  function left(row, modal) {
    if (modal) { closeModal(); return; }
    if (!row) return;
    if (row.hasAttribute("data-step")) {
      if (isFoldable(row) && row.dataset.folded !== "1") setFold(row, true);
      return;
    }
    const st = stepFor(row);
    if (st && !row.classList.contains("d0")) { setFold(st, true); select(st); return; }
    goBack();
  }
  function goBack() {
    const parent = $("a[data-parent]");
    if (parent) { location.href = parent.href; return; }
    const sessions = $("a[data-back]");
    if (sessions) location.href = sessions.href;
  }

  // ---- keyboard ----
  // While the detail modal is open, only these keys act; the rest belong to the dialog.
  const MODAL_KEYS = new Set(["j", "k", "ArrowDown", "ArrowUp", "e", "Escape", "h", "ArrowLeft"]);
  document.addEventListener("keydown", e => {
    const t = e.target;
    if (t.closest("input, textarea, select, [contenteditable]")) {
      if (e.key === "Escape") t.blur();
      return;
    }
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const row = current();
    const modal = pane() && pane().matches(":modal");
    if (modal && e.key === "Enter" && !t.closest("button, a")) { e.preventDefault(); return; }
    if (modal && !MODAL_KEYS.has(e.key)) return;
    switch (e.key) {
      case "j": case "ArrowDown": move(1); break;
      case "k": case "ArrowUp": move(-1); break;
      case "g": select(rows()[0]); break;
      case "G": { const l = rows(); select(l[l.length - 1]); break; }
      case "h": case "ArrowLeft": left(row, modal); break;
      case "l": case "ArrowRight": right(row); break;
      case "Enter": open(row); break;
      case "Escape": if (modal) closeModal(); else return; break;
      case "e": { const b = $("[data-expand]"); if (b) b.click(); break; }
      case "/": { const q = $("#tq") || $("#q"); if (q) { e.preventDefault(); q.focus(); } break; }
      case "p": { const a = $("a[data-parent]"); if (a) location.href = a.href; break; }
      case "i": case "u": case "F": { const b = $(`[data-tab][data-key="${e.key}"]`); if (b) b.click(); break; }
      default: return;
    }
    e.preventDefault();
  });

  document.addEventListener("cancel", e => {
    if (e.target === pane()) { e.preventDefault(); closeModal(); }
  }, true);

  // Keep the selected row after htmx appends more rows; mark the trace tab active after swaps.
  document.addEventListener("htmx:afterSettle", e => {
    if (e.target && e.target.id === "trace") {
      const t = $("[data-tab]"); if (t) $$("[data-tab]").forEach(x => x.setAttribute("aria-selected", String(x === t)));
    }
  });
})();
