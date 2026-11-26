// Generic, accessible UI pieces: tabs, modal dialogs, drag resizers, and
// request-failure reporting. Feature modules use these; nothing here knows
// about OpenFGA.
import { $, $$, load, session, stateKey, store } from './core.js';

// ---- tabs: <fga-tabs> (WAI-ARIA tabs pattern: roving tabindex, arrows, Home/End) ----
//
// The element is the tablist. The server renders it with role="tablist" and
// its [role=tab] buttons; each tab names its panel with aria-controls (the
// panel may be anywhere on the page) and may have:
//   data-tab  a name to select it by (otherwise its id)
//   data-src  a URL that fills the panel the first time it is shown
// The markup's aria-selected="true" tab is selected first. On the tablist,
// data-remember="key" keeps the choice for this browser tab and store
// (data-remember-in="local": for this browser), and reselects it next time.
//
// The selected tab is marked by aria-selected only (CSS styles that), never a
// class: when htmx settles a swap it puts back the server's class attributes.
//
// Pages select tabs with select(), and hear about every selection from the
// "fgax:tab" event (detail: { name, panel }), sent from the tablist.

export class FgaTabs extends HTMLElement {
  connectedCallback() {
    if (!this.wired) {
      this.wired = true;
      this.addEventListener('click', (e) => {
        const tab = e.target instanceof Element ? e.target.closest('[role="tab"]') : null;
        if (tab && this.contains(tab)) this.select(tab);
      });
      this.addEventListener('keydown', (e) => this.onKey(e));
    }
    const saved = this.recall();
    this.select((saved && this.tab(saved)) || $('[role="tab"][aria-selected="true"]', this) || this.tabs[0]);
  }

  /** @returns {HTMLElement[]} */
  get tabs() { return $$('[role="tab"]', this); }
  /** @returns {string} the selected tab's name */
  get selected() {
    const t = $('[role="tab"][aria-selected="true"]', this);
    return t ? this.nameOf(t) : '';
  }
  /** @param {Element} tab */
  nameOf(tab) { return /** @type {HTMLElement} */ (tab).dataset.tab || tab.id; }
  /** @param {string} name @returns {HTMLElement | undefined} */
  tab(name) { return this.tabs.find((t) => this.nameOf(t) === name); }
  /** @param {Element} tab @returns {HTMLElement | null} */
  panelOf(tab) { return document.getElementById(tab.getAttribute('aria-controls') || ''); }

  /**
   * Selects a tab (by name, the tab, or its panel) and shows its panel,
   * loading the panel first if it has never been shown.
   * @param {string | Element | undefined | null} which
   * @returns {Promise<void>} when the panel is ready
   */
  select(which) {
    const tab = typeof which === 'string' ? this.tab(which)
      : this.tabs.find((t) => t === which || this.panelOf(t) === which);
    if (!tab) return Promise.resolve();
    for (const t of this.tabs) {
      const on = t === tab;
      t.setAttribute('aria-selected', String(on));
      t.tabIndex = on ? 0 : -1;
      const p = this.panelOf(t);
      if (p) p.hidden = !on;
    }
    const name = this.nameOf(tab);
    this.remember(name);
    const panel = this.panelOf(tab);
    this.dispatchEvent(new CustomEvent('fgax:tab', { bubbles: true, detail: { name, panel } }));
    if (tab.dataset.src && panel && !panel.dataset.loaded) {
      panel.dataset.loaded = '1';
      return load(tab.dataset.src, panel);
    }
    return Promise.resolve();
  }

  /** Arrow keys, Home and End move between tabs, selecting the one they reach. @param {KeyboardEvent} e */
  onKey(e) {
    const tabs = this.tabs;
    const i = tabs.indexOf(/** @type {HTMLElement} */ (e.target));
    if (i < 0) return;
    const next = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
    if (next === undefined) return;
    e.preventDefault();
    const t = tabs[(next + tabs.length) % tabs.length];
    t.focus();
    this.select(t);
  }

  get storage() { return this.dataset.rememberIn === 'local' ? store : session; }
  get key() { const k = this.dataset.remember || ''; return this.dataset.rememberIn === 'local' ? k : stateKey(k); }
  /** @returns {string | null} */
  recall() { return this.dataset.remember ? this.storage.get(this.key, null) : null; }
  /** @param {string} name */
  remember(name) { if (this.dataset.remember) this.storage.set(this.key, name); }
}

/** Defines <fga-tabs>, which sets up the tablists already on the page and every one added later. */
export function initTabs() {
  if (!customElements.get('fga-tabs')) customElements.define('fga-tabs', FgaTabs);
}

/**
 * The tablist holding an element (a tab), or matching a selector.
 * @param {string | Element | null} which
 * @returns {FgaTabs | null}
 */
export const tabsOf = (which) => /** @type {FgaTabs | null} */ (
  (typeof which === 'string' ? $(which) : which)?.closest('fga-tabs') ?? null);

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

/**
 * "show N more" under a list of users: reveals the rest of the same list (so
 * it scrolls as one) and toggles back to the first few.
 */
export function initShowMore() {
  document.addEventListener('click', (e) => {
    const btn = e.target instanceof Element ? e.target.closest('[data-act="lu-more"]') : null;
    const list = btn?.previousElementSibling;
    if (!btn || !list) return;
    const open = btn.getAttribute('aria-expanded') !== 'true';
    const extra = $$('.lu-extra', list);
    extra.forEach((li) => { /** @type {HTMLElement} */ (li).hidden = !open; });
    list.classList.toggle('lu-open', open);
    btn.setAttribute('aria-expanded', String(open));
    btn.textContent = open ? 'show fewer' : /** @type {HTMLElement} */ (btn).dataset.more || 'show more';
    if (!open) list.scrollTop = 0;
  });
}
