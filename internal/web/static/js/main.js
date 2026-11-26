// OpenFGA Explorer client entry point. Pages are server-rendered and swapped
// by htmx; these modules add the graphs, the walk replay, pickers, and
// keyboard support. Each module owns its own state; this file only starts
// them.
//
// Content htmx loads later needs no wiring here: widgets are custom
// elements (<fga-picker>, <fga-tabs>, <fga-trace>, …) that set themselves up
// when they arrive, and pages act on what they load once load() (core.js)
// resolves.
import { initCheckForm } from './checkform.js';
import { initContext } from './context.js';
import { initExplore } from './explore.js';
import { initPickers } from './pickers.js';
import { initScenarios } from './scenarios.js';
import { initChanges } from './changes.js';
import { initPalette } from './palette.js';
import { initShortcuts } from './shortcuts.js';
import { initTrace } from './trace.js';
import { initRequestErrors, initShowMore, initTabs } from './ui.js';

if (window.cytoscape && window.cytoscapeDagre) {
  try { cytoscape.use(cytoscapeDagre); } catch { /* already registered */ }
}

function init() {
  initShortcuts();
  initTabs();
  initShowMore();
  initRequestErrors();
  initPickers();
  initContext();
  initExplore();
  initCheckForm();
  initScenarios();
  initChanges();
  initPalette();
  initTrace(); // after initCheckForm, which hears each result's fgax:trace
  // Lets tests (and anything else) know the page is fully wired.
  document.documentElement.dataset.ready = 'true';
}

// Module scripts run after parsing; DOMContentLoaded may already have fired.
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
else init();
