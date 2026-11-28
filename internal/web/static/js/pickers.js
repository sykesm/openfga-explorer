// @ts-check
// Instance pickers: <fga-picker>, a custom element. The server renders its
// parts: a type (a <select>, or a find-as-you-type combobox when the model
// has many types) plus an ID search whose results the server renders. The
// element adds the behavior: the browser calls connectedCallback whenever a
// picker is added to the page (the first page, or content htmx loads), so a
// picker sets itself up wherever it appears.
//
// Pages talk to a picker through its methods (open, set, hint) and hear from
// it through events, so they need not know its parts:
//   fgax:picked        an instance was chosen; the page decides what that
//                      means (Explore opens it, Check remembers the form)
//   fgax:type-changed  the type changed (Check refreshes its relations)
//
// The server renders the parts the element expects (layout.html, "picker"):
// .picker-type (a <select>, the source of truth for the type), an optional
// .tcombo (type combobox: .tcombo-q and .tcombo-list), .picker-q (the ID
// search box), .picker-value (the chosen "type:id") and .picker-results.
import { $, $$, base, recentObjects, store } from './core.js';
import { Listbox } from './combobox.js';
import { pushRecent, rankTypes, relTag, splitRels } from './typematch.js';
import { onEscape } from './shortcuts.js';

/** @typedef {{ value: string, text: string, mode: string }} PickedDetail */

const closeResults = () => $$('.picker-results').forEach((r) => { r.innerHTML = ''; });

export class FgaPicker extends HTMLElement {
  connectedCallback() {
    this.hint();
    if (this.wired) return; // moved, not new: its listeners are already on it
    this.wired = true;
    const combo = $('.tcombo', this);
    if (combo) new TypeCombo(combo);
    this.addEventListener('click', (e) => {
      const btn = e.target instanceof Element && e.target.closest('.pick');
      if (btn instanceof HTMLElement) this.choose(btn);
    });
    this.addEventListener('input', (e) => { if (e.target === this.q) this.valueInput.value = ''; });
    this.addEventListener('change', (e) => { if (e.target === this.typeSelect) this.typeChanged(); });
    this.addEventListener('keydown', (e) => { if (e.target === this.q) this.onKey(e); });
    // An empty search lists the type's recent objects first: send them (they
    // are kept in this browser, not on the server).
    this.addEventListener('htmx:configRequest', (/** @type {any} */ e) => {
      if (e.detail.elt === this.q && !this.q.value.trim()) e.detail.parameters.recent = JSON.stringify(recentObjects());
    });
    // "Keep scanning" appends results: drop the note it came from.
    this.addEventListener('htmx:afterRequest', (/** @type {any} */ e) => {
      const btn = e.detail.elt;
      if (btn?.classList?.contains('keep-scanning') && e.detail.successful) btn.closest('.sr-note')?.remove();
    });
  }

  get field() { return this.dataset.field || ''; }
  get mode() { return this.dataset.mode || ''; }
  /** @returns {HTMLInputElement} the ID search box */
  get q() { return $('.picker-q', this); }
  /** @returns {HTMLSelectElement} the type: the source of truth, even under a combobox */
  get typeSelect() { return $('.picker-type', this); }
  /** @returns {HTMLInputElement} the chosen "type:id", submitted with the form */
  get valueInput() { return $('.picker-value', this); }
  /** @returns {HTMLElement} */
  get results() { return $('.picker-results', this); }

  /** Updates the ID placeholder (per-type hint) and the combobox label. */
  hint() {
    const opt = this.typeSelect?.selectedOptions[0];
    if (this.q) this.q.placeholder = opt?.dataset.hint || 'type an ID or prefix…';
    const combo = $('.tcombo-q', this);
    if (combo && document.activeElement !== combo) combo.value = opt?.textContent || '';
  }

  /**
   * Opens the results: focusing fires the search, but a box that already has
   * focus gets no focus event, so trigger the search directly.
   */
  open() {
    if (document.activeElement === this.q) htmx.trigger(this.q, 'focus');
    else this.q.focus();
  }

  /**
   * Sets the chosen instance without a search (and its type, which the value
   * names: "type:id", or "type:id#relation" for a userset).
   * @param {string} value
   * @param {string} text what the ID box shows
   */
  set(value, text) {
    const colon = value.indexOf(':');
    const hash = value.lastIndexOf('#');
    const type = value.slice(0, colon);
    const want = hash > colon ? `${type}#${value.slice(hash + 1)}` : type;
    if ([...this.typeSelect.options].some((o) => o.value === want)) this.typeSelect.value = want;
    this.valueInput.value = value;
    this.q.value = text;
    this.hint();
  }

  /** A new type clears the instance, announces the change, and searches the new type. */
  typeChanged() {
    this.hint();
    this.valueInput.value = '';
    this.dispatchEvent(new CustomEvent('fgax:type-changed', { bubbles: true, detail: { field: this.field } }));
    htmx.trigger(this.q, 'input');
  }

  /** @param {HTMLElement} btn a search result */
  choose(btn) {
    this.valueInput.value = btn.dataset.value || '';
    this.q.value = btn.dataset.text || '';
    this.results.innerHTML = '';
    /** @type {PickedDetail} */
    const detail = { value: btn.dataset.value || '', text: btn.dataset.text || '', mode: this.mode };
    this.dispatchEvent(new CustomEvent('fgax:picked', { bubbles: true, detail }));
  }

  /** ↑/↓ move through results, Enter chooses, Escape closes. @param {KeyboardEvent} e */
  onKey(e) {
    const items = $$('.pick', this);
    const cur = items.findIndex((b) => b.classList.contains('active'));
    /** @param {number} i */
    const move = (i) => {
      items.forEach((b) => b.classList.remove('active'));
      if (items[i]) { items[i].classList.add('active'); items[i].scrollIntoView({ block: 'nearest' }); }
    };
    if (e.key === 'ArrowDown') { e.preventDefault(); move(Math.min(items.length - 1, cur + 1)); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); move(Math.max(0, cur - 1)); }
    else if (e.key === 'Escape') { this.results.innerHTML = ''; e.stopPropagation(); }
    else if (e.key === 'Enter' && items.length && this.mode === 'explore') { e.preventDefault(); this.choose(items[Math.max(0, cur)]); }
    else if (e.key === 'Enter' && cur >= 0) { e.preventDefault(); this.choose(items[cur]); }
  }
}

/**
 * A picker on the page, if any: the first inside scope, for field if given.
 * @param {string} scope a selector, e.g. '#check-form'
 * @param {string} [field] e.g. 'user'
 * @returns {FgaPicker | null}
 */
export const picker = (scope, field = '') =>
  /** @type {FgaPicker | null} */ ($(`${scope} fga-picker${field ? `[data-field="${field}"]` : ''}`));

/** Every picker inside an element. @param {ParentNode} root @returns {FgaPicker[]} */
export const pickersIn = (root) => /** @type {FgaPicker[]} */ ($$('fga-picker', root));

// ---- type combobox (combobox.js) ----
// The <select> stays, hidden, as the source of truth: choosing sets it and
// fires "change", so everything that reads the type is unchanged.
const MAX_COMBO = 80;

class TypeCombo {
  /** @param {HTMLElement} box */
  constructor(box) {
    this.picker = /** @type {FgaPicker} */ (box.closest('fga-picker'));
    /** @type {HTMLInputElement} */
    this.input = $('.tcombo-q', box);
    /** @type {HTMLSelectElement} */
    this.select = this.picker.typeSelect;
    this.list = new Listbox(this.input, $('.tcombo-list', box), (v) => this.choose(v));
    this.input.addEventListener('focus', () => { this.input.select(); this.open(''); });
    this.input.addEventListener('click', () => { if (!this.list.open) this.open(''); });
    this.input.addEventListener('input', () => this.open(this.input.value));
    this.input.addEventListener('keydown', (e) => this.onKey(e));
    // Enter's keypress is the form's implicit submit. Browsers skip it after
    // the keydown is handled, but synthesized key events (automation, some
    // assistive tools) may still send it: never submit from here.
    this.input.addEventListener('keypress', (e) => { if (e.key === 'Enter') e.preventDefault(); });
    // Closes a moment after the box loses focus (so a click on an option
    // lands first), unless focus has come back by then.
    this.input.addEventListener('blur', () => setTimeout(() => { if (document.activeElement !== this.input) this.close(); }, 120));
    this.select.addEventListener('change', () => this.sync());
  }
  get key() { return `recentPick:${this.picker.field}:${base()}`; }
  options() {
    return Array.from(this.select.options).map((o) => ({ value: o.value, label: o.textContent || '', rels: splitRels(o.dataset.rels) }));
  }
  /** @param {string} q */
  open(q) {
    closeResults(); // one dropdown at a time
    const all = this.options();
    const ranked = rankTypes(all, q);
    /** @type {import('./combobox.js').Row[]} */
    const rows = [];
    if (!q.trim()) {
      const recent = store.get(this.key, []).map((/** @type {string} */ v) => all.find((o) => o.value === v)).filter(Boolean).slice(0, 5);
      if (recent.length) {
        rows.push({ section: 'recent' }, ...recent, { section: `all ${all.length} types` });
      }
    }
    rows.push(...ranked.slice(0, MAX_COMBO).map(({ o, m }) => ({ value: o.value, label: o.label, tag: relTag(m.rels) })));
    if (ranked.length > MAX_COMBO) rows.push({ note: `…${ranked.length - MAX_COMBO} more: keep typing to narrow` });
    if (!ranked.length) rows.push({ note: `no type or relation matches “${q}”` });
    // With a query, the best match is ready for Enter; without, the current type.
    const options = rows.filter((r) => 'value' in r);
    this.list.show(rows, q.trim() ? 0 : options.findIndex((o) => 'value' in o && o.value === this.select.value));
  }
  /** @param {KeyboardEvent} e */
  onKey(e) {
    if (this.list.onKey(e)) return;
    const chosen = this.list.value;
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); this.open(''); break; // closed: open it
      // The type is one field of several: Enter here never submits the form
      // (with an active option, the list chose it above).
      case 'Enter': e.preventDefault(); break;
      case 'Escape': if (this.list.open) { e.stopPropagation(); this.close(); } break;
      case 'Tab': // Tab accepts a typed match, then moves on
        if (this.list.open && this.input.value !== this.label() && chosen !== undefined) this.choose(chosen);
        break;
      default:
    }
  }
  label() { return this.select.selectedOptions[0]?.textContent || ''; }
  /** @param {string} value */
  choose(value) {
    const changed = this.select.value !== value;
    this.select.value = value;
    store.set(this.key, pushRecent(store.get(this.key, []), value));
    this.close();
    if (changed) this.select.dispatchEvent(new Event('change', { bubbles: true }));
    // On to the ID box once this key event is over, so its keypress (above)
    // lands here, not in the ID box, where Enter submits.
    setTimeout(() => this.picker.q?.focus(), 0);
  }
  close() {
    this.list.hide();
    this.sync();
  }
  sync() { if (document.activeElement !== this.input || !this.list.open) this.input.value = this.label(); }
}

/**
 * Defines <fga-picker>, which sets up the pickers already on the page (and
 * every one added later), and closes open results on Escape or a click
 * outside any picker.
 */
export function initPickers() {
  if (!customElements.get('fga-picker')) customElements.define('fga-picker', FgaPicker);
  onEscape(closeResults);
  document.addEventListener('click', (e) => {
    if (!(e.target instanceof Element) || !e.target.closest('fga-picker')) closeResults();
  });
}
