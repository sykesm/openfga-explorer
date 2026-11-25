// The command palette (Ctrl+K / ⌘K): pages, actions, and every type of the
// model (matched by name or relation, like the type finder). The type index
// is fetched once per page, on first open.
import { $, $$, base, esc } from './core.js';
import { rankTypes, relTag, typeWords } from './typematch.js';
import { ModalDialog } from './ui.js';

/** @typedef {{ label: string, hint: string, href?: string, act?: string }} Item */

/** @returns {Item[]} */
const pages = () => [
  { label: 'Explore', hint: 'types, instances, and their edges', href: `${base()}/explore` },
  { label: 'Check', hint: 'explain why a user has (or lacks) access', href: `${base()}/check` },
  { label: 'Scenarios', hint: 'tuples and expectations, in a sandbox', href: `${base()}/scenarios` },
  { label: 'Changes', hint: 'tuple writes and deletes over time', href: `${base()}/changes?range=1h` },
  { label: 'Model', hint: 'the model, its modules, and model checks', href: `${base()}/model` },
  { label: 'What changed in this version?', hint: 'compare with the previous model', href: `${base()}/model/diff` },
  { label: 'Keyboard shortcuts', hint: 'help', act: 'help' },
];

/** @type {{ label: string, rels: string[], module: string }[] | null} */
let index = null;
/** @type {Item[]} */
let items = [];
let active = 0;

async function loadIndex() {
  if (index) return index;
  try {
    const r = await fetch(`${base()}/api/types`);
    index = (await r.json()).map((/** @type {any} */ e) => ({ label: e.t, rels: e.r || [], module: e.m || '' }));
  } catch { index = []; }
  return index;
}

/** @param {string} q */
function render(q) {
  const words = typeWords(q);
  const list = $('#palette-list');
  const pageItems = pages().filter((p) => words.every((w) => `${p.label} ${p.hint}`.toLowerCase().includes(w)));
  const types = rankTypes(index || [], q).slice(0, words.length ? 40 : 8).map(({ o, m }) => ({
    label: o.label,
    hint: m.rels.length ? relTag(m.rels) : `type${o.module ? ` · module ${o.module}` : ''}`,
    href: `${base()}/explore?type=${encodeURIComponent(o.label)}`,
  }));
  items = [...pageItems, ...types];
  list.innerHTML = items.length
    ? items.map((it, i) => `<li id="pal-${i}" role="option" aria-selected="false" data-i="${i}">${esc(it.label)} <span class="muted sm">${esc(it.hint)}</span></li>`).join('')
    : `<li class="muted" role="presentation">Nothing matches “${esc(q)}”.</li>`;
  move(0);
}

/** @param {number} i */
function move(i) {
  const opts = $$('#palette-list [role="option"]');
  opts.forEach((o) => o.setAttribute('aria-selected', 'false'));
  active = Math.max(0, Math.min(i, opts.length - 1));
  const o = opts[active];
  const q = $('.palette-q');
  if (o) { o.setAttribute('aria-selected', 'true'); o.scrollIntoView({ block: 'nearest' }); q.setAttribute('aria-activedescendant', o.id); }
  else q.removeAttribute('aria-activedescendant');
}

/** @param {ModalDialog} dlg @param {number} i */
function go(dlg, i) {
  const it = items[i];
  if (!it) return;
  dlg.close();
  if (it.href) location.assign(it.href);
  else if (it.act) /** @type {HTMLElement} */ ($(`[data-act="${it.act}"]`))?.click();
}

export function initPalette() {
  const el = $('.palette-overlay');
  if (!el || !document.body.dataset.base) return;
  const dlg = new ModalDialog(el);
  const q = $('.palette-q', el);
  const open = async () => {
    q.value = '';
    dlg.open();
    render('');
    await loadIndex();
    render(q.value);
  };
  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); if (dlg.isOpen) dlg.close(); else open(); }
  });
  document.addEventListener('click', (e) => {
    if (e.target instanceof Element && e.target.closest('[data-act="palette"]')) open();
  });
  q.addEventListener('input', () => render(q.value));
  q.addEventListener('keydown', (/** @type {KeyboardEvent} */ e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); move(active + 1); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); move(active - 1); }
    else if (e.key === 'Enter') { e.preventDefault(); go(dlg, active); }
  });
  $('#palette-list', el).addEventListener('click', (/** @type {MouseEvent} */ e) => {
    const li = /** @type {Element} */ (e.target).closest('[data-i]');
    if (li instanceof HTMLElement) go(dlg, Number(li.dataset.i));
  });
}
