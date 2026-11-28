// @ts-check
// The popup list of a find-as-you-type box (WAI-ARIA combobox with a listbox
// popup), shared by the picker's type box and the Explore type finder. It
// renders the options, keeps one active (aria-selected on the option,
// aria-activedescendant on the box), moves it with ↑/↓, and chooses with
// Enter or a click. What to list, and what choosing means, is the caller's.
import { $$, esc } from './core.js';

/**
 * A row: an option, or a section heading or note (not choosable).
 * @typedef {{ value: string, label: string, tag?: string } | { section: string } | { note: string }} Row
 */

export class Listbox {
  /**
   * @param {HTMLInputElement} input the box (role="combobox", aria-controls the list)
   * @param {HTMLElement} list the popup (role="listbox", with an id)
   * @param {(value: string) => void} onChoose
   */
  constructor(input, list, onChoose) {
    this.input = input;
    this.list = list;
    this.onChoose = onChoose;
    /** @type {string[]} the options' values, in order */
    this.values = [];
    this.active = -1;
    // mousedown, not click: choose before the box's blur closes the list.
    list.addEventListener('mousedown', (e) => {
      const li = e.target instanceof Element ? e.target.closest('li[role="option"]') : null;
      if (li instanceof HTMLElement) { e.preventDefault(); this.onChoose(li.dataset.value || ''); }
    });
  }

  get open() { return !this.list.hidden; }
  /** @returns {string | undefined} the active option's value */
  get value() { return this.values[this.active]; }

  /**
   * Shows rows, with the option at index active (of the options) active.
   * @param {Row[]} rows
   * @param {number} [active]
   */
  show(rows, active = 0) {
    this.values = [];
    const html = rows.map((r) => {
      if ('section' in r) return `<li class="sec" role="presentation">${esc(r.section)}</li>`;
      if ('note' in r) return `<li class="more" role="presentation">${esc(r.note)}</li>`;
      const id = `${this.list.id}-${this.values.length}`;
      this.values.push(r.value);
      return `<li id="${id}" role="option" data-value="${esc(r.value)}" aria-selected="false">${esc(r.label)}${r.tag ? `<span class="rel-tag">${esc(r.tag)}</span>` : ''}</li>`;
    });
    this.list.innerHTML = html.join('');
    this.list.hidden = rows.length === 0;
    this.input.setAttribute('aria-expanded', String(this.open));
    this.move(active);
  }

  hide() {
    this.list.hidden = true;
    this.input.setAttribute('aria-expanded', 'false');
    this.input.removeAttribute('aria-activedescendant');
    this.active = -1;
  }

  /** Makes option i active (none when out of range). @param {number} i */
  move(i) {
    const opts = $$('li[role="option"]', this.list);
    opts.forEach((o) => o.setAttribute('aria-selected', 'false'));
    this.active = i >= 0 && i < opts.length ? i : -1;
    const o = opts[this.active];
    if (o) {
      o.setAttribute('aria-selected', 'true');
      o.scrollIntoView({ block: 'nearest' });
      this.input.setAttribute('aria-activedescendant', o.id);
    } else this.input.removeAttribute('aria-activedescendant');
  }

  /**
   * Handles ↑/↓ (while open) and Enter (on an active option).
   * @param {KeyboardEvent} e
   * @returns {boolean} whether it handled the key
   */
  onKey(e) {
    if (!this.open) return false;
    const last = this.values.length - 1;
    if (e.key === 'ArrowDown') { e.preventDefault(); this.move(Math.min(last, this.active + 1)); return true; }
    if (e.key === 'ArrowUp') { e.preventDefault(); this.move(Math.max(0, this.active - 1)); return true; }
    if (e.key === 'Enter' && this.value !== undefined) { e.preventDefault(); this.onChoose(this.value); return true; }
    return false;
  }
}
