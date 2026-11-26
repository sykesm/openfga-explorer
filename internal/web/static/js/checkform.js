// The Check form: per-tab persistence, collapse to a summary after a run,
// live walk progress and cancel, re-running with other options, and stepping
// through "who else has this?" users.
import { $, $$, base, session, stateKey } from './core.js';
import { openPicker, pickerHint, refreshRelations } from './pickers.js';
import { onKey } from './shortcuts.js';

const form = () => /** @type {HTMLFormElement | null} */ ($('#check-form'));
/** @param {HTMLFormElement} f */
const formFields = (f) => /** @type {any[]} */ (Array.from(f.elements)).filter((el) => el.name && el.name !== 'field' && el.name !== 'mode');

// Set while re-running a restored check, so the result shows without a replay.
let restoring = false;
// The "who else has this?" user type to re-list after tracing one of its users.
/** @type {string | null} */
let relist = null;
/** @type {EventSource | null} */
let progress = null;

/** True once after a restored check's result arrives (and resets). */
export function consumeRestoring() {
  const r = restoring;
  restoring = false;
  return r;
}

/** @param {{ ran?: boolean }} [opts] */
export function saveCheck({ ran } = {}) {
  const f = form();
  if (!f) return;
  const prev = session.get(stateKey('check')) || {};
  /** @type {Record<string, unknown>} */
  const fields = {};
  for (const el of formFields(f)) fields[el.name] = el.type === 'checkbox' ? el.checked : el.value;
  // Editing the form after a run means the shown result no longer matches it.
  session.set(stateKey('check'), { fields, ran: ran ?? (prev.ran && JSON.stringify(prev.fields) === JSON.stringify(fields)) });
}

function restoreCheck() {
  const f = form();
  if (!f) return;
  const p = new URLSearchParams(location.search);
  if (p.get('user') || p.get('object') || p.get('relation')) return; // an explicit link wins
  const saved = session.get(stateKey('check'));
  if (!saved?.fields) return;
  for (const el of formFields(f)) {
    if (!(el.name in saved.fields)) continue;
    if (el.type === 'checkbox') el.checked = !!saved.fields[el.name];
    else if (el.name !== 'relation') el.value = saved.fields[el.name];
  }
  $$('.picker', f).forEach(pickerHint);
  refreshRelations(saved.fields.relation || '').then(() => {
    if (saved.ran) { restoring = true; htmx.trigger(f, 'submit'); }
  });
}

/** Collapses the form to the server's one-line summary of the check. @param {boolean} on */
function collapseForm(on) {
  const f = form();
  if (!f) return;
  const summary = $('#check-result .verdict')?.dataset.summary;
  if (on && !summary) return;
  f.classList.toggle('collapsed', on);
  if (summary) $('.qsummary .qs-text', f).textContent = summary;
}

/**
 * Re-opens the form for editing. Focus goes to the form itself, not the User
 * box: focusing a picker opens its instance list over the form ("/" does
 * that on purpose). Tab moves into the fields.
 */
function editCheck() {
  collapseForm(false);
  form()?.focus();
}

/**
 * Shows (or hides) an option's help under the options; one at a time.
 * @param {HTMLElement} btn
 */
function toggleOptionHelp(btn) {
  const help = document.getElementById(btn.getAttribute('aria-controls') || '');
  if (!help) return;
  const open = help.hidden;
  for (const b of $$('[data-act="opt-help"]')) {
    const h = document.getElementById(b.getAttribute('aria-controls') || '');
    if (h) h.hidden = true;
    b.setAttribute('aria-expanded', 'false');
  }
  help.hidden = !open;
  btn.setAttribute('aria-expanded', String(open));
}

/** Re-runs the check with a field changed. @param {(f: HTMLFormElement) => void} change */
function rerun(change) {
  const f = form();
  if (!f) return;
  change(f);
  htmx.trigger(f, 'submit');
}

/** Re-traces the check for a listed user, then re-lists that user type. @param {HTMLElement} btn */
function traceUser(btn) {
  const f = form();
  if (!f) return;
  const u = btn.dataset.user || '';
  const colon = u.indexOf(':');
  const hash = u.lastIndexOf('#');
  const type = u.slice(0, colon);
  const want = hash > colon ? `${type}#${u.slice(hash + 1)}` : type;
  const sel = $('select[name="user_type"]', f);
  if (sel && [...sel.options].some((o) => o.value === want)) sel.value = want;
  $('input[name="user"]', f).value = u;
  $('input[name="user_q"]', f).value = btn.dataset.text;
  $$('.picker', f).forEach(pickerHint);
  relist = $('#rpanel-others select[name="user_type"]')?.value || null;
  saveCheck();
  htmx.trigger(f, 'submit');
  window.scrollTo({ top: 0 });
}

/** Re-lists the same user type in the "Who else?" tab after tracing one of its users. */
function reopenOthers() {
  if (relist === null) return;
  const list = $('#rpanel-others form.whohas');
  if (list) {
    $('#rtab-others')?.click();
    const sel = $('select[name="user_type"]', list);
    if (sel) sel.value = relist;
    htmx.trigger(list, 'submit');
  }
  relist = null;
}

/** Streams walk progress into the spinner while a check runs. @param {any} e htmx:configRequest */
function startProgress(e) {
  const id = Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) => (b % 36).toString(36)).join('');
  e.detail.parameters.run_id = id;
  progress?.close();
  const text = $('#check-spinner .spin-text');
  progress = new EventSource(`${base()}/check/progress?id=${id}`);
  progress.addEventListener('progress', (m) => {
    const p = JSON.parse(m.data);
    if (text) text.textContent = `walking… ${p.calls} calls${p.current ? ` · ${p.current}` : ''}`;
  });
  progress.addEventListener('done', () => progress?.close());
}

function stopProgress() {
  progress?.close();
  progress = null;
  const text = $('#check-spinner .spin-text');
  if (text) text.textContent = 'walking…';
}

/** @param {MouseEvent} e */
function onClick(e) {
  const t = /** @type {Element} */ (e.target);
  const act = /** @type {HTMLElement | null} */ (t.closest('[data-act]'));
  const user = t.closest('.lu-trace');
  if (user instanceof HTMLElement && form()) { e.preventDefault(); traceUser(user); return; }
  if (!act || !form()) return;
  switch (act.dataset.act) {
    case 'cancel-check':
      htmx.trigger('#check-form', 'htmx:abort');
      stopProgress();
      $('#check-result')?.insertAdjacentHTML('afterbegin', '<div class="alert warn" role="status">Walk canceled.</div>');
      break;
    case 'order': rerun((f) => { $('select[name="order"]', f).value = act.dataset.order; }); break;
    case 'all-paths': rerun((f) => { $('select[name="order"]', f).value = 'all'; }); break;
    case 'bigger':
      rerun((f) => {
        for (const [name, cap] of /** @type {[string, number][]} */ ([['max_calls', 5000], ['max_fanout', 1000], ['probe_limit', 1000]])) {
          const el = $(`input[name="${name}"]`, f);
          if (el) el.value = Math.min(cap, (+el.value || +el.defaultValue) * 4);
        }
      });
      break;
    case 'opt-help': toggleOptionHelp(act); break;
    case 'edit-check': editCheck(); break;
    case 'reset-check': session.del(stateKey('check')); location.assign(`${base()}/check`); break;
    default:
  }
}

export function initCheckForm() {
  const f = form();
  if (!f) return;
  restoreCheck();
  f.addEventListener('input', () => saveCheck());
  f.addEventListener('change', () => saveCheck());
  document.addEventListener('click', onClick);
  document.addEventListener('fgax:picked', (/** @type {any} */ e) => { if (e.detail.mode !== 'explore') saveCheck(); });
  document.addEventListener('htmx:configRequest', (/** @type {any} */ e) => { if (e.detail.elt?.id === 'check-form') startProgress(e); });
  document.addEventListener('htmx:afterRequest', (/** @type {any} */ e) => {
    if (e.detail.elt?.id !== 'check-form') return;
    stopProgress();
    if (e.detail.successful) collapseForm(true);
  });
  onKey('edit', editCheck);
  onKey('find', () => {
    if (f.classList.contains('collapsed')) collapseForm(false);
    const q = $('#check-form .picker-q');
    if (q) openPicker(q);
  });
}

/** After a check result is swapped in. @param {Element} t */
export function checkSwapped(t) {
  if (t.id === 'check-result' && $('.verdict', t)) saveCheck({ ran: true });
}

/** After htmx has processed a result (its list form needs htmx handlers). @param {Element} t */
export function checkSettled(t) {
  if (t.id === 'check-result') reopenOthers();
}
