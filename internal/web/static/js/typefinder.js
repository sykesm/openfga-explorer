// The Explore type finder for models with many types: a search whose matches
// (by type name or relation) are listed as you type, the types you used
// recently (each removable), and the most connected types as starting
// points. Small models show every type as a chip instead (no finder).
import { $, $$, base, esc, store } from './core.js';
import { pushRecent, rankTypes, relTag } from './typematch.js';

const recentKey = () => `recentTypes:${base()}`;
const MAX_RESULTS = 50;
const MAX_RECENT = 8;

/** @type {{ label: string, rels: string[], module: string, extendedBy: string[] }[]} */
let index = [];
/** @type {(t: string) => void} */
let pick = () => {};
let active = -1;

const recents = () => /** @type {string[]} */ (store.get(recentKey(), [])).filter((t) => index.some((o) => o.label === t));

/** @param {string} [current] the selected type, marked in the list */
function renderRecent(current) {
  const box = $('.type-recent');
  if (!box) return;
  const names = recents();
  $('.ts-list', box).innerHTML = names.map((t) => `<li>
      <button type="button" class="type-chip${t === current ? ' on' : ''}" data-type="${esc(t)}">${esc(t)}</button>
      <button type="button" class="ts-remove ghost sm" data-remove="${esc(t)}" aria-label="Remove ${esc(t)} from recent types" title="Remove from recent types">×</button>
    </li>`).join('');
  box.hidden = names.length === 0;
}

/** Records t as the most recent type and marks it selected. @param {string} t */
export function rememberType(t) {
  $$('.type-chip').forEach((c) => c.classList.toggle('on', c.dataset.type === t));
  $(`.type-list .type-chip[data-type="${CSS.escape(t)}"]`)?.scrollIntoView({ block: 'nearest' });
  if (!$('.type-recent')) return;
  store.set(recentKey(), pushRecent(store.get(recentKey(), []), t, MAX_RECENT));
  renderRecent(t);
}

/** @param {string} t */
function forget(t) {
  store.set(recentKey(), store.get(recentKey(), []).filter((/** @type {string} */ x) => x !== t));
  renderRecent($('.type-chip.on')?.dataset.type);
}

/** Lists the types matching the query (or shows the starting points when empty). @param {string} q */
function search(q) {
  const input = $('.type-q');
  const list = $('.type-results');
  const count = $('.type-count');
  const start = $('.type-start');
  active = -1;
  if (!q.trim()) {
    list.hidden = true;
    list.innerHTML = '';
    start.hidden = false;
    count.textContent = '';
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
    return;
  }
  // "module:<name>" lists the types a module defines, then those it extends.
  const mod = /^module:(\S+)$/i.exec(q.trim())?.[1];
  const ranked = mod
    ? [...index.filter((o) => o.module === mod).map((o) => ({ o, m: { score: 0, rels: [] } })),
      ...index.filter((o) => o.extendedBy.includes(mod)).map((o) => ({ o, m: { score: 1, rels: [`extended by ${mod}`] } }))]
    : rankTypes(index, q);
  start.hidden = true;
  count.textContent = ranked.length
    ? `${ranked.length} matching type${ranked.length === 1 ? '' : 's'}${ranked.length > MAX_RESULTS ? ` (first ${MAX_RESULTS} shown; keep typing)` : ''}`
    : `No type or relation matches “${q}”.`;
  list.innerHTML = ranked.slice(0, MAX_RESULTS).map(({ o, m }, i) => {
    const tag = relTag(m.rels);
    return `<li id="type-opt-${i}" role="option" aria-selected="false" data-type="${esc(o.label)}">${esc(o.label)}${tag ? `<span class="rel-tag">${esc(tag)}</span>` : ''}</li>`;
  }).join('');
  list.hidden = ranked.length === 0;
  input.setAttribute('aria-expanded', String(ranked.length > 0));
  move(0);
}

/** @param {number} i */
function move(i) {
  const opts = $$('.type-results [role="option"]');
  const input = $('.type-q');
  opts.forEach((o) => o.setAttribute('aria-selected', 'false'));
  active = Math.max(-1, Math.min(i, opts.length - 1));
  const o = opts[active];
  if (o) {
    o.setAttribute('aria-selected', 'true');
    o.scrollIntoView({ block: 'nearest' });
    input.setAttribute('aria-activedescendant', o.id);
  } else input.removeAttribute('aria-activedescendant');
}

/** @param {string} t */
function choose(t) {
  const input = $('.type-q');
  input.value = '';
  search('');
  pick(t);
}

/**
 * @param {(t: string) => void} onPick opens a type (search results only;
 *   chips are plain .type-chip buttons the page already handles)
 */
export function initTypeFinder(onPick) {
  const input = $('.type-q');
  if (!input) return;
  pick = onPick;
  index = JSON.parse($('#type-index')?.textContent || '[]').map((/** @type {any} */ e) => ({ label: e.t, rels: e.r || [], module: e.m || '', extendedBy: e.x || [] }));
  renderRecent();
  input.addEventListener('input', () => search(input.value));
  input.addEventListener('keydown', (/** @type {KeyboardEvent} */ e) => {
    const opts = $$('.type-results [role="option"]');
    if (e.key === 'ArrowDown') { e.preventDefault(); move(active + 1); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); move(active - 1); }
    else if (e.key === 'Enter' && opts[active]) { e.preventDefault(); choose(opts[active].dataset.type); }
    else if (e.key === 'Escape' && input.value) { e.stopPropagation(); input.value = ''; search(''); }
  });
  // mousedown: choose before the input loses focus.
  $('.type-results').addEventListener('mousedown', (/** @type {MouseEvent} */ e) => {
    const o = /** @type {Element} */ (e.target).closest('[role="option"]');
    if (o instanceof HTMLElement) { e.preventDefault(); choose(o.dataset.type || ''); }
  });
  $('.type-find').addEventListener('click', (/** @type {MouseEvent} */ e) => {
    const t = /** @type {Element} */ (e.target);
    const rm = t.closest('[data-remove]');
    if (rm instanceof HTMLElement) { forget(rm.dataset.remove || ''); return; }
    if (t.closest('[data-act="clear-recent-types"]')) { store.set(recentKey(), []); renderRecent(); }
    const mod = t.closest('.mod-chip');
    if (mod instanceof HTMLElement) {
      input.value = `module:${mod.dataset.module}`;
      search(input.value);
      input.focus();
    }
  });
}
