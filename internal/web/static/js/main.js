// OpenFGA Explorer client entry point. Pages are server-rendered and swapped
// by htmx; these modules add the graphs, the walk replay, pickers, and
// keyboard support. Each module owns its own state; this file only wires
// page lifecycle events to them.
import { $$ } from './core.js';
import { checkSettled, checkSwapped, consumeRestoring, initCheckForm } from './checkform.js';
import { initContext } from './context.js';
import { exploreSettled, exploreSwapped, initExplore } from './explore.js';
import { initPickers, pickerHint } from './pickers.js';
import { initScenarios } from './scenarios.js';
import { initChanges } from './changes.js';
import { initPalette } from './palette.js';
import { initShortcuts } from './shortcuts.js';
import { initTraceKeys, renderTrace } from './trace.js';
import { initPlainTabs, initRequestErrors, initTabKeys } from './ui.js';

if (window.cytoscape && window.cytoscapeDagre) {
  try { cytoscape.use(cytoscapeDagre); } catch { /* already registered */ }
}

function init() {
  initShortcuts();
  initTabKeys();
  initPlainTabs();
  initRequestErrors();
  initPickers();
  initContext();
  initExplore();
  initCheckForm();
  initScenarios();
  initChanges();
  initPalette();
  initTraceKeys();
  renderTrace();
  // Lets tests (and anything else) know the page is fully wired.
  document.documentElement.dataset.ready = 'true';
}

// Module scripts run after parsing; DOMContentLoaded may already have fired.
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
else init();

document.addEventListener('htmx:afterSwap', (/** @type {any} */ e) => {
  const t = e.detail.target;
  if (!t) return;
  $$('.picker', t).forEach(pickerHint);
  exploreSwapped(t);
  if (t.id === 'check-result') { const restoring = consumeRestoring(); renderTrace({ replay: !restoring, restore: restoring }); }
  checkSwapped(t);
});

document.addEventListener('htmx:afterSettle', (/** @type {any} */ e) => {
  const t = e.detail.target;
  if (!t) return;
  exploreSettled(t);
  checkSettled(t);
});

// "Keep scanning" appends results; drop the old incomplete note.
document.addEventListener('htmx:afterRequest', (/** @type {any} */ e) => {
  const el = e.detail.elt;
  if (el?.classList?.contains('keep-scanning') && e.detail.successful) el.closest('.sr-note')?.remove();
});
