// @ts-check
// The shared request context (condition parameters): edited in the header
// popover or the check form, kept per store, and sent with "who has it?"
// lists that don't carry their own.
import { $, base, store } from './core.js';
import { onEscape } from './shortcuts.js';

const key = () => `ctx:${base()}`;
/** @returns {string} */
export const sharedContext = () => store.get(key(), '');

/**
 * @param {string} text JSON object text, or empty to clear
 * @param {{ fromForm?: boolean }} [opts]
 */
function setSharedContext(text, { fromForm = false } = {}) {
  const t = (text || '').trim();
  const err = $('.ctx-err');
  if (t) {
    try { JSON.parse(t); if (err) err.textContent = ''; } catch { if (err) err.textContent = 'not valid JSON'; return; }
  } else if (err) err.textContent = '';
  store.set(key(), t);
  const badge = $('.ctx-badge');
  if (badge) badge.hidden = !t;
  const pop = $('.ctx-text');
  if (pop && pop.value !== t && document.activeElement !== pop) pop.value = t;
  const field = $('#check-form textarea[name="context"]');
  if (field && !fromForm && field.value !== t) {
    field.value = t;
    field.dispatchEvent(new Event('input', { bubbles: true })); // the form saves itself
  }
}

/** @param {boolean} open @param {{ focusButton?: boolean }} [opts] */
function showPopover(open, { focusButton = false } = {}) {
  const pop = $('.ctx-pop');
  const btn = $('[data-act="context"]');
  if (!pop || !btn) return;
  pop.hidden = !open;
  btn.setAttribute('aria-expanded', String(open));
  if (open) $('.ctx-text', pop)?.focus();
  else if (focusButton) btn.focus();
}

export function initContext() {
  const field = $('#check-form textarea[name="context"]');
  if (field) {
    if (field.value.trim()) setSharedContext(field.value, { fromForm: true }); // a link's context wins
    else if (sharedContext()) field.value = sharedContext();
    field.addEventListener('input', () => setSharedContext(field.value, { fromForm: true }));
  }
  setSharedContext(sharedContext(), { fromForm: true });
  $('.ctx-text')?.addEventListener('input', (/** @type {any} */ e) => setSharedContext(e.target.value));
  onEscape(() => { if (!$('.ctx-pop')?.hidden) showPopover(false, { focusButton: true }); });
  document.addEventListener('click', (e) => {
    const t = /** @type {Element} */ (e.target);
    if (t.closest('[data-act="context"]')) { showPopover(!!$('.ctx-pop')?.hidden); return; }
    if (t.closest('[data-act="ctx-now"]')) {
      let obj = {};
      try { obj = JSON.parse(sharedContext() || '{}'); } catch { /* replace invalid */ }
      setSharedContext(JSON.stringify({ ...obj, current_time: new Date().toISOString().replace(/\.\d+Z$/, 'Z') }));
      return;
    }
    if (t.closest('[data-act="ctx-clear"]')) { setSharedContext(''); return; }
    if (!t.closest('.ctx-wrap') && !$('.ctx-pop')?.hidden) showPopover(false);
  });
  // "Who has it?" lists use the shared request context unless one is given.
  document.addEventListener('htmx:configRequest', (/** @type {any} */ e) => {
    if ((e.detail.path.includes('/explore/listusers') || e.detail.path.includes('/explore/access')) && !e.detail.parameters.context) {
      const ctx = sharedContext();
      if (ctx) e.detail.parameters.context = ctx;
    }
  });
}
