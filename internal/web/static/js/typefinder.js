// <fga-type-finder>: the Explore type finder for models with many types. A
// search whose matches (by type name or relation; "module:<name>" for a
// module's types) are listed as you type (combobox.js), and, while it is
// empty, starting points: the types you used recently (each removable),
// modules, and the most connected types. Small models show every type as a
// chip instead (no finder).
//
// The server renders its parts (explore.html): .type-q (the box),
// #type-results (the list), .type-count, .type-start (starting points) with
// .type-recent, and the type index as JSON in a <script> child.
//
//   fgax:type-chosen  a search result was chosen (detail: { type }). The
//                     starting points are .type-chip[data-type] buttons,
//                     which the page handles like every type chip.
//   remember(type)    records a type as the most recent, marked as current
//   open()            moves focus to the search box
import { $, base, esc, store } from './core.js';
import { Listbox } from './combobox.js';
import { pushRecent, rankTypes, relTag } from './typematch.js';

const MAX_RESULTS = 50;
const MAX_RECENT = 8;

/** @typedef {{ label: string, rels: string[], module: string, extendedBy: string[] }} TypeEntry */

class FgaTypeFinder extends HTMLElement {
  connectedCallback() {
    if (this.wired) return;
    this.wired = true;
    /** @type {HTMLInputElement} */
    this.input = $('.type-q', this);
    /** @type {TypeEntry[]} */
    this.index = JSON.parse($('script', this)?.textContent || '[]')
      .map((/** @type {any} */ e) => ({ label: e.t, rels: e.r || [], module: e.m || '', extendedBy: e.x || [] }));
    this.list = new Listbox(this.input, $('.type-results', this), (t) => this.choose(t));
    this.renderRecent();
    this.input.addEventListener('input', () => this.search(this.input.value));
    this.input.addEventListener('keydown', (e) => {
      if (this.list.onKey(e)) return;
      if (e.key === 'Escape' && this.input.value) { e.stopPropagation(); this.clear(); }
    });
    this.addEventListener('click', (e) => this.onClick(e));
  }

  get key() { return `recentTypes:${base()}`; }
  /** @returns {string[]} recent types that are in this model */
  get recent() { return /** @type {string[]} */ (store.get(this.key, [])).filter((t) => this.index.some((o) => o.label === t)); }

  open() { this.input.focus(); }

  /** Records t as the most recent type and marks it current. @param {string} t */
  remember(t) {
    store.set(this.key, pushRecent(store.get(this.key, []), t, MAX_RECENT));
    this.renderRecent(t);
  }

  /** @param {string} t */
  forget(t) {
    store.set(this.key, store.get(this.key, []).filter((/** @type {string} */ x) => x !== t));
    this.renderRecent($('.type-recent .type-chip.on', this)?.dataset.type);
  }

  /** @param {string} [current] the selected type, marked in the list */
  renderRecent(current) {
    const box = $('.type-recent', this);
    if (!box) return;
    const names = this.recent;
    $('.ts-list', box).innerHTML = names.map((t) => `<li>
        <button type="button" class="type-chip${t === current ? ' on' : ''}" data-type="${esc(t)}">${esc(t)}</button>
        <button type="button" class="ts-remove ghost sm" data-remove="${esc(t)}" aria-label="Remove ${esc(t)} from recent types" title="Remove from recent types">×</button>
      </li>`).join('');
    box.hidden = names.length === 0;
  }

  /** Lists the types matching the query, or shows the starting points when empty. @param {string} q */
  search(q) {
    const count = $('.type-count', this);
    const start = $('.type-start', this);
    if (!q.trim()) {
      this.list.show([]);
      start.hidden = false;
      count.textContent = '';
      return;
    }
    // "module:<name>" lists the types a module defines, then those it extends.
    const mod = /^module:(\S+)$/i.exec(q.trim())?.[1];
    const ranked = mod
      ? [...this.index.filter((o) => o.module === mod).map((o) => ({ o, m: { score: 0, rels: [] } })),
        ...this.index.filter((o) => o.extendedBy.includes(mod)).map((o) => ({ o, m: { score: 1, rels: [`extended by ${mod}`] } }))]
      : rankTypes(this.index, q);
    start.hidden = true;
    count.textContent = ranked.length
      ? `${ranked.length} matching type${ranked.length === 1 ? '' : 's'}${ranked.length > MAX_RESULTS ? ` (first ${MAX_RESULTS} shown; keep typing)` : ''}`
      : `No type or relation matches “${q}”.`;
    this.list.show(ranked.slice(0, MAX_RESULTS).map(({ o, m }) => ({ value: o.label, label: o.label, tag: relTag(m.rels) })));
  }

  clear() {
    this.input.value = '';
    this.search('');
  }

  /** @param {string} t */
  choose(t) {
    this.clear();
    this.dispatchEvent(new CustomEvent('fgax:type-chosen', { bubbles: true, detail: { type: t } }));
  }

  /** @param {MouseEvent} e */
  onClick(e) {
    const t = /** @type {Element} */ (e.target);
    const rm = t.closest('[data-remove]');
    if (rm instanceof HTMLElement) { this.forget(rm.dataset.remove || ''); return; }
    if (t.closest('[data-act="clear-recent-types"]')) { store.set(this.key, []); this.renderRecent(); return; }
    const mod = t.closest('.mod-chip');
    if (mod instanceof HTMLElement) {
      this.input.value = `module:${mod.dataset.module}`;
      this.search(this.input.value);
      this.input.focus();
    }
  }
}

/** Defines <fga-type-finder>. */
export function initTypeFinder() {
  if (!customElements.get('fga-type-finder')) customElements.define('fga-type-finder', FgaTypeFinder);
}
