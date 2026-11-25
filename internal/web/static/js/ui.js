// Generic, accessible UI pieces: tabs, modal dialogs, drag resizers, and
// request-failure reporting. Feature modules use these; nothing here knows
// about OpenFGA.
import { $, $$, store } from './core.js';

// ---- tabs (WAI-ARIA tabs pattern: roving tabindex, arrows, Home/End) ----

/**
 * Marks tab as the selected tab of its tablist.
 * @param {Element} tab
 */
export function selectTab(tab) {
  const list = tab.closest('[role="tablist"]');
  if (!list) return;
  for (const t of $$('[role="tab"]', list)) {
    const on = t === tab;
    t.classList.toggle('on', on);
    t.setAttribute('aria-selected', String(on));
    t.tabIndex = on ? 0 : -1;
  }
}

/** Arrow keys move between tabs; the focused tab is activated (clicked). */
export function initTabKeys() {
  document.addEventListener('keydown', (e) => {
    const tab = e.target instanceof Element ? e.target.closest('[role="tab"]') : null;
    if (!tab) return;
    const tabs = $$('[role="tab"]', /** @type {Element} */ (tab.closest('[role="tablist"]')));
    const i = tabs.indexOf(tab);
    const next = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
    if (next === undefined) return;
    e.preventDefault();
    const t = tabs[(next + tabs.length) % tabs.length];
    t.focus();
    t.click();
  });
}

/**
 * Plain tab lists (marked data-tabs): a tab shows the panel it controls
 * (aria-controls) and hides its siblings' panels.
 */
export function initPlainTabs() {
  document.addEventListener('click', (e) => {
    const tab = e.target instanceof Element ? e.target.closest('[data-tabs] [role="tab"]') : null;
    if (!tab) return;
    selectTab(tab);
    for (const t of $$('[role="tab"]', /** @type {Element} */ (tab.closest('[data-tabs]')))) {
      const panel = document.getElementById(t.getAttribute('aria-controls') || '');
      if (panel) panel.hidden = t !== tab;
    }
  });
}

// ---- modal dialog: focus moves in, stays in, and returns on close ----

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export class ModalDialog {
  /** @param {HTMLElement} el the overlay; its [role=dialog] child holds the content */
  constructor(el) {
    this.el = el;
    /** @type {Element | null} */
    this.returnTo = null;
    el.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { e.stopPropagation(); this.close(); return; }
      if (e.key !== 'Tab') return;
      const f = $$(FOCUSABLE, el).filter((x) => x.offsetParent !== null);
      if (!f.length) return;
      const first = f[0], last = f[f.length - 1];
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    });
    // A click on the backdrop (outside the dialog) closes it.
    el.addEventListener('click', (e) => { if (e.target === el) this.close(); });
  }
  get isOpen() { return !this.el.hidden; }
  open() {
    if (this.isOpen) return;
    this.returnTo = document.activeElement;
    this.el.hidden = false;
    ($('[autofocus]', this.el) || $(FOCUSABLE, this.el))?.focus();
  }
  close() {
    if (!this.isOpen) return;
    this.el.hidden = true;
    if (this.returnTo instanceof HTMLElement && this.returnTo.isConnected) this.returnTo.focus();
    this.returnTo = null;
  }
}

// ---- drag handles (explore side panel, trace text drawer) ----

/**
 * @param {HTMLElement | null} handle the separator element
 * @param {(x: number) => number} apply sets the size for a pointer x and returns the width to store
 * @param {(w: number) => unknown} restore sets a stored width
 * @param {string} key preference key
 * @param {() => void} [done] called after a resize (e.g. to resize graphs)
 */
export function initResizer(handle, apply, restore, key, done = () => {}) {
  if (!handle) return;
  const saved = store.get(key, null);
  if (saved) restore(saved);
  let dragging = false;
  handle.addEventListener('pointerdown', (e) => { dragging = true; handle.setPointerCapture(e.pointerId); });
  handle.addEventListener('pointermove', (e) => { if (dragging) store.set(key, apply(e.clientX)); });
  handle.addEventListener('pointerup', () => { dragging = false; done(); });
  handle.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    e.preventDefault();
    const r = handle.getBoundingClientRect();
    store.set(key, apply(r.left + (e.key === 'ArrowLeft' ? -24 : 24)));
    done();
  });
}

// ---- request failures ----

/**
 * Shows a dismissible message in the page-level alert region.
 * @param {string} text
 */
export function notify(text) {
  const box = $('#page-alert');
  if (!box) return;
  $('.pa-text', box).textContent = text;
  box.hidden = false;
}

/** Server errors replace the target's content; network failures are announced. */
export function initRequestErrors() {
  document.addEventListener('htmx:responseError', (/** @type {any} */ e) => {
    const t = e.detail.target;
    const body = e.detail.xhr?.responseText;
    // Containers show the server's error page fragment in place; form
    // controls (e.g. the relation <select>) can't, so announce instead.
    if (t && body && !t.matches('select, input, textarea')) t.innerHTML = body;
    else notify(`The server could not complete the request (HTTP ${e.detail.xhr?.status ?? '?'}).`);
  });
  const unreachable = () => notify('Could not reach the explorer server. Check the connection (or SSH tunnel) and try again.');
  document.addEventListener('htmx:sendError', unreachable);
  document.addEventListener('htmx:timeout', unreachable);
  document.addEventListener('click', (e) => {
    if (e.target instanceof Element && e.target.closest('#page-alert [data-act="dismiss"]')) {
      /** @type {HTMLElement} */ ($('#page-alert')).hidden = true;
    }
  });
}
