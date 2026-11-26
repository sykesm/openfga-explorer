// Instance pickers: a type (a <select>, or a find-as-you-type combobox when
// the model has many types) plus an ID search whose results the server
// renders. Choosing an instance dispatches "fgax:picked" on the picker; the
// page decides what that means (Explore opens it, Check remembers the form).
import { $, $$, base, esc, load, store } from './core.js';
import { pushRecent, rankTypes, relTag, splitRels } from './typematch.js';
import { onEscape } from './shortcuts.js';

/** @typedef {{ value: string, text: string, mode: string }} PickedDetail */

const closeResults = () => $$('.picker-results').forEach((r) => { r.innerHTML = ''; });

/** Updates the ID placeholder (per-type hint) and the combobox label. @param {Element} picker */
export function pickerHint(picker) {
  const sel = $('.picker-type', picker);
  const q = $('.picker-q', picker);
  const opt = sel?.selectedOptions[0];
  if (q) q.placeholder = opt?.dataset.hint || 'type an ID or prefix…';
  const combo = $('.tcombo-q', picker);
  if (combo && document.activeElement !== combo) combo.value = opt?.textContent || '';
}

/**
 * Opens the picker's results: focusing fires its search, but an input that
 * already has focus gets no focus event, so trigger the search directly.
 * @param {HTMLInputElement} q
 */
export function openPicker(q) {
  if (document.activeElement === q) htmx.trigger(q, 'focus');
  else q.focus();
}

/**
 * Reloads the relation options for the current user and object types, so
 * relations the user type can never reach are disabled.
 * @param {string} [relation]
 */
export function refreshRelations(relation) {
  const sel = $('#relation-select');
  const q = new URLSearchParams({
    object_type: $('select[name="object_type"]')?.value || '',
    user_type: $('select[name="user_type"]')?.value || '',
    relation: relation ?? sel.value,
  });
  return load(`${base()}/relations?${q}`, sel);
}

/** @param {HTMLElement} btn a search result */
function choose(btn) {
  const picker = /** @type {HTMLElement} */ (btn.closest('.picker'));
  $('.picker-value', picker).value = btn.dataset.value;
  $('.picker-q', picker).value = btn.dataset.text;
  $('.picker-results', picker).innerHTML = '';
  /** @type {PickedDetail} */
  const detail = { value: btn.dataset.value || '', text: btn.dataset.text || '', mode: picker.dataset.mode || '' };
  picker.dispatchEvent(new CustomEvent('fgax:picked', { bubbles: true, detail }));
}

// ---- type combobox (WAI-ARIA combobox with listbox popup) ----
// The <select> stays, hidden, as the source of truth: choosing sets it and
// fires "change", so everything that reads the type is unchanged.
const MAX_COMBO = 80;

export class TypeCombo {
  /** @param {HTMLElement} box */
  constructor(box) {
    this.picker = /** @type {HTMLElement} */ (box.closest('.picker'));
    /** @type {HTMLInputElement} */
    this.input = $('.tcombo-q', box);
    /** @type {HTMLUListElement} */
    this.list = $('.tcombo-list', box);
    /** @type {HTMLSelectElement} */
    this.select = $('.picker-type', this.picker);
    /** @type {string[]} */
    this.items = [];
    this.active = -1;
    this.input.addEventListener('focus', () => { this.input.select(); this.open(''); });
    this.input.addEventListener('click', () => { if (this.list.hidden) this.open(''); });
    this.input.addEventListener('input', () => this.open(this.input.value));
    this.input.addEventListener('keydown', (e) => this.onKey(e));
    this.input.addEventListener('blur', () => setTimeout(() => this.close(), 120));
    // mousedown, not click: choose before the input's blur closes the list.
    this.list.addEventListener('mousedown', (e) => {
      const li = e.target instanceof Element && e.target.closest('li[data-value]');
      if (li instanceof HTMLElement) { e.preventDefault(); this.choose(li.dataset.value || ''); }
    });
    this.select.addEventListener('change', () => this.sync());
  }
  get key() { return `recentPick:${this.picker.dataset.field}:${base()}`; }
  options() {
    return Array.from(this.select.options).map((o) => ({ value: o.value, label: o.textContent || '', rels: splitRels(o.dataset.rels) }));
  }
  /** @param {string} q */
  open(q) {
    closeResults(); // one dropdown at a time
    const all = this.options();
    const ranked = rankTypes(all, q);
    const rows = [];
    this.items = [];
    /** @param {{ value: string, label: string }} o @param {string[]} [rels] */
    const add = (o, rels = []) => {
      const tag = relTag(rels);
      rows.push(`<li id="${this.list.id}-${this.items.length}" role="option" data-value="${esc(o.value)}" aria-selected="false">${esc(o.label)}${tag ? `<span class="rel-tag">${esc(tag)}</span>` : ''}</li>`);
      this.items.push(o.value);
    };
    if (!q.trim()) {
      const recent = store.get(this.key, []).map((/** @type {string} */ v) => all.find((o) => o.value === v)).filter(Boolean).slice(0, 5);
      if (recent.length) {
        rows.push('<li class="sec" role="presentation">recent</li>');
        recent.forEach((/** @type {any} */ o) => add(o));
        rows.push(`<li class="sec" role="presentation">all ${all.length} types</li>`);
      }
    }
    ranked.slice(0, MAX_COMBO).forEach(({ o, m }) => add(o, m.rels));
    if (ranked.length > MAX_COMBO) rows.push(`<li class="more" role="presentation">…${ranked.length - MAX_COMBO} more: keep typing to narrow</li>`);
    if (!ranked.length) rows.push(`<li class="more" role="presentation">no type or relation matches “${esc(q)}”</li>`);
    this.list.innerHTML = rows.join('');
    this.list.hidden = false;
    this.input.setAttribute('aria-expanded', 'true');
    // With a query, the best match is ready for Enter; without, the current type.
    this.move(q.trim() ? 0 : this.items.indexOf(this.select.value));
  }
  /** @param {number} i */
  move(i) {
    const lis = $$('li[data-value]', this.list);
    lis.forEach((li) => li.setAttribute('aria-selected', 'false'));
    this.active = i;
    const li = lis[i];
    if (li) {
      li.setAttribute('aria-selected', 'true');
      li.scrollIntoView({ block: 'nearest' });
      this.input.setAttribute('aria-activedescendant', li.id);
    } else this.input.removeAttribute('aria-activedescendant');
  }
  /** @param {KeyboardEvent} e */
  onKey(e) {
    const chosen = this.items[this.active];
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        if (this.list.hidden) this.open(''); else this.move(Math.min(this.items.length - 1, this.active + 1));
        break;
      case 'ArrowUp': e.preventDefault(); this.move(Math.max(0, this.active - 1)); break;
      case 'Enter': if (!this.list.hidden && chosen !== undefined) { e.preventDefault(); this.choose(chosen); } break;
      case 'Escape': if (!this.list.hidden) { e.stopPropagation(); this.close(); } break;
      case 'Tab': // Tab accepts a typed match, then moves on
        if (!this.list.hidden && this.input.value !== this.label() && chosen !== undefined) this.choose(chosen);
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
    $('.picker-q', this.picker)?.focus();
  }
  close() {
    this.list.hidden = true;
    this.input.setAttribute('aria-expanded', 'false');
    this.input.removeAttribute('aria-activedescendant');
    this.sync();
  }
  sync() { if (document.activeElement !== this.input || this.list.hidden) this.input.value = this.label(); }
}

export function initPickers() {
  $$('.picker').forEach(pickerHint);
  $$('.tcombo').forEach((b) => new TypeCombo(b));
  onEscape(closeResults);
  document.addEventListener('click', (e) => {
    const t = /** @type {Element} */ (e.target);
    const btn = t.closest('.pick');
    if (btn) { choose(/** @type {HTMLElement} */ (btn)); return; }
    if (!t.closest('.picker')) closeResults();
  });
  document.addEventListener('input', (e) => {
    const t = /** @type {Element} */ (e.target);
    if (t.matches('.picker-q')) $('.picker-value', t.closest('.picker')).value = '';
  });
  document.addEventListener('change', (e) => {
    const t = /** @type {Element} */ (e.target);
    if (!t.matches('.picker-type')) return;
    const picker = /** @type {HTMLElement} */ (t.closest('.picker'));
    pickerHint(picker);
    $('.picker-value', picker).value = '';
    if ((picker.dataset.field === 'object' || picker.dataset.field === 'user') && $('#relation-select')) refreshRelations();
    htmx.trigger($('.picker-q', picker), 'input');
  });
  // ↑/↓ move through results, Enter chooses, Escape closes.
  document.addEventListener('keydown', (e) => {
    if (!(e.target instanceof Element) || !e.target.matches('.picker-q')) return;
    const picker = /** @type {HTMLElement} */ (e.target.closest('.picker'));
    const items = $$('.pick', picker);
    const cur = items.findIndex((b) => b.classList.contains('active'));
    /** @param {number} i */
    const move = (i) => {
      items.forEach((b) => b.classList.remove('active'));
      if (items[i]) { items[i].classList.add('active'); items[i].scrollIntoView({ block: 'nearest' }); }
    };
    if (e.key === 'ArrowDown') { e.preventDefault(); move(Math.min(items.length - 1, cur + 1)); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); move(Math.max(0, cur - 1)); }
    else if (e.key === 'Escape') { $('.picker-results', picker).innerHTML = ''; e.stopPropagation(); }
    else if (e.key === 'Enter' && items.length && picker.dataset.mode === 'explore') { e.preventDefault(); choose(items[Math.max(0, cur)]); }
    else if (e.key === 'Enter' && cur >= 0) { e.preventDefault(); choose(items[cur]); }
  });
}
