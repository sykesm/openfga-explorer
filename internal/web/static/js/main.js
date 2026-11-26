// OpenFGA Explorer client entry point. Pages are server-rendered and swapped
// by htmx; these modules add the graphs, the walk replay, pickers, and
// keyboard support. Each module owns its own state; this file only wires
// page lifecycle events to them.
import { $$ } from './core.js';
import { checkLoaded, consumeRestoring, initCheckForm } from './checkform.js';
import { initContext } from './context.js';
import { exploreLoaded, initExplore } from './explore.js';
import { initPickers, pickerHint } from './pickers.js';
import { initScenarios } from './scenarios.js';
import { initChanges } from './changes.js';
import { initPalette } from './palette.js';
import { initShortcuts } from './shortcuts.js';
import { initTraceKeys, renderTrace } from './trace.js';
import { initPlainTabs, initRequestErrors, initShowMore, initTabKeys } from './ui.js';

if (window.cytoscape && window.cytoscapeDagre) {
  try { cytoscape.use(cytoscapeDagre); } catch { /* already registered */ }
}

function init() {
  initShortcuts();
  initTabKeys();
  initPlainTabs();
  initShowMore();
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

// New content is set up in htmx.onLoad: htmx calls it for each element it
// inserts, after the swap has settled and htmx has processed the element's
// hx-* attributes (earlier, in htmx:afterSwap, settling would still put back
// the server's class attributes, and the new controls would not work yet).
// One swap can insert several elements: they are set up together, once per
// region (the element they went into), before load() (core.js) resolves.
/** @type {Element[]} */
let inserted = [];
htmx.onLoad((/** @type {Element} */ el) => {
  if (el === document.body || !el.parentElement) return; // the first page: init()
  if (!inserted.length) queueMicrotask(setUp);
  inserted.push(el);
});

function setUp() {
  const regions = new Set(inserted.map((el) => el.parentElement));
  inserted = [];
  for (const t of regions) {
    if (!t || !t.isConnected) continue;
    $$('.picker', t).forEach(pickerHint);
    exploreLoaded(t);
    if (t.id === 'check-result') { const restoring = consumeRestoring(); renderTrace({ replay: !restoring, restore: restoring }); }
    checkLoaded(t);
  }
}

// "Keep scanning" appends results; drop the old incomplete note.
document.addEventListener('htmx:afterRequest', (/** @type {any} */ e) => {
  const el = e.detail.elt;
  if (el?.classList?.contains('keep-scanning') && e.detail.successful) el.closest('.sr-note')?.remove();
});
