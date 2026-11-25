// The Scenarios page: the document is a draft kept in this browser (per
// store); runs go to the server, which keeps nothing. Results link each check
// to the tuples that granted it.
import { $, $$, base, store } from './core.js';

const draftKey = () => `scenario:${base()}`;

/** A file name for the document, from its `name:` line. @param {string} text */
function fileName(text) {
  const name = /^name:\s*["']?(.+?)["']?\s*$/m.exec(text)?.[1] || 'scenario';
  return `${name.toLowerCase().replace(/[^\w.-]+/g, '-').replace(/^-+|-+$/g, '') || 'scenario'}.fga.yaml`;
}

/** @param {HTMLElement} article @param {string[]} tuples @param {boolean} on */
function highlightTuples(article, tuples, on) {
  for (const i of tuples) $(`[data-tuple="${CSS.escape(i)}"]`, article)?.classList.toggle('hl', on);
}

/**
 * Adds the shown check to the Scenarios draft (kept in this browser): the
 * server builds the test from the check and merges it into the draft.
 * @param {HTMLElement} btn
 */
async function capture(btn) {
  const v = /** @type {HTMLElement} */ (btn.closest('.verdict'));
  const msg = $('.capture-msg', v);
  const body = new URLSearchParams({
    user: v.dataset.user || '', relation: v.dataset.relation || '', object: v.dataset.object || '',
    context: v.dataset.context || '', contextual: v.dataset.contextual || '',
    draft: store.get(draftKey(), '') || '',
  });
  if ($('[data-act="capture-mask"]', v)?.checked) body.set('mask', '1');
  btn.disabled = true;
  msg.textContent = 'adding…';
  try {
    const r = await fetch(`${base()}/scenarios/capture`, { method: 'POST', body });
    const res = await r.json();
    if (!r.ok || res.error) throw new Error(res.error || `HTTP ${r.status}`);
    store.set(draftKey(), res.draft);
    msg.replaceChildren(`Added “${res.name}” (${res.tuples} tuple${res.tuples === 1 ? '' : 's'}) to your scenario draft · `);
    const a = document.createElement('a');
    a.href = `${base()}/scenarios`;
    a.textContent = 'Open Scenarios';
    msg.append(a);
  } catch (e) {
    msg.textContent = `Not added: ${e instanceof Error ? e.message : e}`;
  } finally {
    btn.disabled = false;
  }
}

export function initScenarios() {
  document.addEventListener('click', (e) => {
    const b = e.target instanceof Element && e.target.closest('.verdict [data-act="capture"]');
    if (b instanceof HTMLElement) capture(b);
  });
  const form = $('#scenario-form');
  if (!form) return;
  const doc = /** @type {HTMLTextAreaElement} */ ($('#scn-doc', form));
  const saved = store.get(draftKey(), null);
  if (typeof saved === 'string' && saved.trim()) doc.value = saved;
  let timer = 0;
  const flush = () => { clearTimeout(timer); store.set(draftKey(), doc.value); };
  const save = () => { clearTimeout(timer); timer = window.setTimeout(flush, 250); };
  doc.addEventListener('input', save);
  // Never lose the last edit: save when running, and when leaving the page.
  form.addEventListener('submit', flush);
  window.addEventListener('pagehide', flush);
  // Ctrl/Cmd+Enter runs.
  doc.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); htmx.trigger(form, 'submit'); }
  });
  $('[data-act="scn-load"]', form)?.addEventListener('change', async (/** @type {any} */ e) => {
    const file = e.target.files?.[0];
    if (!file) return;
    doc.value = await file.text();
    store.set(draftKey(), doc.value);
    e.target.value = '';
  });
  form.addEventListener('click', (e) => {
    const t = /** @type {Element} */ (e.target);
    if (t.closest('[data-act="scn-download"]')) {
      const a = document.createElement('a');
      a.href = URL.createObjectURL(new Blob([doc.value], { type: 'application/yaml' }));
      a.download = fileName(doc.value);
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 2000);
    }
    if (t.closest('[data-act="scn-template"]') && (doc.value === doc.dataset.template || confirm('Replace the document with a starter for this model? Download it first to keep it.'))) {
      doc.value = doc.dataset.template || '';
      store.set(draftKey(), doc.value);
    }
  });
  // A check highlights the tuples that granted it; a tuple, the checks it granted.
  const result = $('#scenario-result');
  const link = (/** @type {Event} */ e, /** @type {boolean} */ on) => {
    const t = /** @type {Element} */ (e.target);
    const row = t.closest?.('tr[data-grants]');
    const article = t.closest?.('.scn-test');
    if (row instanceof HTMLElement && article instanceof HTMLElement) {
      row.classList.toggle('hl', on);
      highlightTuples(article, (row.dataset.grants || '').trim().split(/\s+/).filter(Boolean), on);
      return;
    }
    const tuple = t.closest?.('[data-tuple]');
    if (tuple instanceof HTMLElement && article instanceof HTMLElement) {
      tuple.classList.toggle('hl', on);
      for (const r of $$('tr[data-grants]', article)) {
        if ((r.dataset.grants || '').trim().split(/\s+/).includes(tuple.dataset.tuple || '')) r.classList.toggle('hl', on);
      }
    }
  };
  result?.addEventListener('mouseover', (e) => link(e, true));
  result?.addEventListener('mouseout', (e) => link(e, false));
  result?.addEventListener('focusin', (e) => link(e, true));
  result?.addEventListener('focusout', (e) => link(e, false));
}
