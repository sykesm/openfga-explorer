// The Explore view's type graph: the model's types and how they relate, a
// chosen type's neighborhood, and (in modular models) coloring by module.
import { $, baseOpts, categoricalColor, dagre, labelWidth, pal, store, typeColor } from './core.js';
import { fitView, graphs, showNote, wasFitted } from './exploregraphs.js';

// Large models start with an empty type graph (see exploreData.Large).
const isLarge = () => $('.explore-layout')?.dataset.large === 'true';
// Whether the type graph shows a neighborhood laid out on its own.
let subLayout = false;

// The "too many types to draw" prompt shows (on the type graph tab) until a
// type is chosen or every type is drawn.
let promptWanted = false;
let noteText = '';
/** Shows the prompt and the note when the type graph tab is showing (they belong to it). */
export const updatePrompt = () => {
  const onSchema = !$('#schema-canvas')?.hidden;
  const el = $('#schema-empty');
  if (el) el.hidden = !(promptWanted && onSchema);
  const note = /** @type {HTMLElement | null} */ ($('#schema-note'));
  if (note) showNote(note, !!noteText && onSchema, graphs.schema);
};
/** @param {boolean} on */
const schemaPrompt = (on) => { promptWanted = on; updatePrompt(); };
// Types shown around a hub at most (see focusType).
const MAX_NEIGHBORS = 40;
/** A one-line note over the type graph (empty hides it). @param {string} text */
const schemaNote = (text) => {
  noteText = text;
  const el = $('#schema-note');
  if (el) el.textContent = text;
  updatePrompt();
};

// Optionally, types are colored by the module that defines them (modular models).
const modules = () => ($('.explore-layout')?.dataset.modules || '').split(',').filter(Boolean);
let byModule = false;
/** @param {any} e a type node */
const nodeColor = (e) => (byModule && e.data('module') ? moduleColor(e.data('module')) : typeColor(e.id()));
/** Modules get the categorical colors in module order. @param {string} m */
const moduleColor = (m) => categoricalColor(Math.max(0, modules().indexOf(m)));

/** Switches type colors between per-type and per-module. @param {boolean} on */
function colorByModule(on) {
  byModule = on;
  store.set('colorByModule', on);
  const legend = /** @type {HTMLElement | null} */ ($('.mod-legend'));
  if (legend) {
    legend.hidden = !on;
    legend.replaceChildren();
    // Built with the DOM (not HTML with style attributes, which the CSP blocks).
    for (const m of on ? modules() : []) {
      const item = document.createElement('span');
      const swatch = document.createElement('i');
      swatch.className = 'nd';
      swatch.style.borderColor = moduleColor(m);
      item.append(swatch, document.createTextNode(m));
      legend.append(item);
    }
  }
  graphs.schema?.style().update();
}

/** @param {ReturnType<typeof pal>} p */
function schemaStyle(p) {
  return [
    { selector: 'node', style: {
      shape: 'round-rectangle', label: 'data(label)', 'font-size': 12, 'font-weight': 600, color: p.fg,
      'text-valign': 'center', 'background-color': p.panel, 'border-width': 2, 'border-color': (/** @type {any} */ e) => nodeColor(e),
      width: (/** @type {any} */ e) => labelWidth(e.data('label'), 12), height: 30,
    } },
    { selector: 'edge', style: {
      'curve-style': 'bezier', 'target-arrow-shape': 'triangle', width: 1.4, 'line-color': p.line, 'target-arrow-color': p.line,
      label: 'data(label)', 'font-size': 9, color: p.muted, 'text-background-color': p.bg, 'text-background-opacity': 0.9,
      'text-background-padding': 2, 'control-point-step-size': 40, 'text-wrap': 'wrap',
    } },
    { selector: 'edge.userset', style: { 'line-style': 'dashed' } },
    { selector: 'edge.wildcard', style: { 'line-style': 'dotted' } },
    { selector: 'edge.ttu', style: { 'line-color': p.ttu, 'target-arrow-color': p.ttu, 'line-style': 'dashed', color: p.ttu } },
    // The second hop recedes without losing legibility (no opacity on labels).
    { selector: 'node.faded', style: { 'border-opacity': 0.4, color: p.muted, 'font-weight': 400 } },
    { selector: 'edge.faded', style: { label: '', 'line-color': p.skipped, 'target-arrow-color': p.skipped, width: 1, 'line-style': 'dotted' } },
    { selector: 'node.sel', style: { 'border-width': 4, 'background-color': (/** @type {any} */ e) => nodeColor(e), color: '#fff' } },
  ];
}

/**
 * Shows the selected type's neighborhood (two hops) in the type graph,
 * hiding the rest until "Show all types" is pressed.
 * @param {string} t
 */
export function focusType(t) {
  const schemaCy = graphs.schema;
  if (!schemaCy) return;
  const n = schemaCy.getElementById(t);
  let capped = 0;
  schemaCy.batch(() => {
    schemaCy.elements().removeClass('sel faded').style('display', 'element');
    if (!n.length) return;
    let hood1 = n.closedNeighborhood();
    // A hub (user, group, … linked to most types) has the whole model as its
    // neighborhood: show its best-connected neighbors only, and say so.
    const neighbors = hood1.nodes().not(n);
    if (neighbors.length > MAX_NEIGHBORS) {
      const degree = (/** @type {any} */ x) => x.neighborhood().nodes().length;
      const keep = neighbors.sort((/** @type {any} */ a, /** @type {any} */ b) => degree(b) - degree(a) || (a.id() < b.id() ? -1 : 1)).slice(0, MAX_NEIGHBORS);
      hood1 = n.union(keep).union(n.connectedEdges().filter((/** @type {any} */ e) => keep.has(e.source()) || keep.has(e.target())));
      capped = neighbors.length;
    }
    // The second hop skips hubs, which would otherwise pull in the whole
    // model; if it is still large, show only the direct neighborhood.
    const hub = Math.max(12, schemaCy.nodes().length / 10);
    const via = hood1.nodes().filter((/** @type {any} */ x) => x === n || x.neighborhood().nodes().length <= hub);
    let hood2 = capped ? hood1 : hood1.union(via.neighborhood());
    if (hood2.nodes().length > 60) hood2 = hood1;
    const far = schemaCy.elements().not(hood2);
    far.style('display', 'none');
    hood2.not(hood1).addClass('faded');
    n.addClass('sel');
    $('.canvas-bar [data-act="show-all-types"]')?.classList.toggle('hidden', far.nodes().length === 0);
  });
  schemaPrompt(false);
  schemaNote(capped ? `${t} is linked to ${capped} types; showing the ${MAX_NEIGHBORS} best connected. Choose one of them, or search on the right.` : '');
  // Positions must come from a layout of what is visible: a neighborhood is
  // laid out on its own; when everything is visible, the full layout is used
  // (run now if it never has been, e.g. in a large model).
  if (schemaCy.nodes(':hidden').length) {
    schemaCy.elements(':visible').layout(dagre('LR', { rankSep: 150, nodeSep: 40, fit: false })).run();
    subLayout = true;
  } else if (subLayout) {
    schemaCy.layout(dagre('LR', { rankSep: 150, nodeSep: 40, fit: false })).run();
    subLayout = false;
  }
  if (/** @type {HTMLElement} */ ($('#schema-canvas')).hidden) return; // fitted when shown
  // Choosing a type keeps the zoom: the type comes to the middle, and the
  // rest of its neighborhood is a scroll away. Only a first view (nothing
  // drawn before, as in a large model) is fitted.
  if (!wasFitted(schemaCy)) {
    fitView(schemaCy, schemaCy.elements(':visible'));
  } else if (n.length) {
    schemaCy.stop(true);
    schemaCy.center(n);
  }
}

/** Shows every type again. */
export function showAllTypes() {
  const schemaCy = graphs.schema;
  if (!schemaCy) return;
  schemaPrompt(false);
  schemaNote('');
  schemaCy.elements().style('display', 'element');
  if (subLayout) {
    subLayout = false;
    schemaCy.layout(dagre('LR', { rankSep: 150, nodeSep: 40, fit: false })).run();
  }
  $('.canvas-bar [data-act="show-all-types"]')?.classList.add('hidden');
  fitView(schemaCy);
}

/**
 * Draws the type graph from the server's elements (api/schema).
 * @param {HTMLElement} container
 * @param {{ elements: any[] }} data
 * @param {{ type(t: string): void, edge(edge: any): void }} on what tapping a type or an edge does
 */
export function initSchemaGraph(container, data, on) {
  const large = isLarge();
  const toggle = /** @type {HTMLInputElement | null} */ ($('[data-act="color-modules"]'));
  if (toggle) {
    toggle.checked = store.get('colorByModule', false);
    byModule = toggle.checked;
    toggle.addEventListener('change', () => colorByModule(toggle.checked));
  }
  // A large model is not laid out (or shown) until asked: only neighborhoods.
  const schemaCy = cytoscape({ container, elements: data.elements, style: schemaStyle(pal()),
    layout: large ? { name: 'null' } : dagre('LR', { rankSep: 150, nodeSep: 40 }), ...baseOpts });
  graphs.schema = schemaCy;
  if (large) {
    schemaCy.elements().style('display', 'none');
    subLayout = true;
    $('.canvas-bar [data-act="show-all-types"]')?.classList.remove('hidden');
    schemaPrompt(true);
  } else fitView(schemaCy);
  if (byModule) colorByModule(true);
  schemaCy.on('tap', 'node', (/** @type {any} */ e) => on.type(e.target.id()));
  schemaCy.on('tap', 'edge', (/** @type {any} */ e) => on.edge(e.target));
  // Tapping the background shows every type again, except in large models
  // (it would bring back the unreadable whole).
  schemaCy.on('tap', (/** @type {any} */ e) => { if (e.target === schemaCy && !large) { schemaCy.elements().removeClass('faded sel'); showAllTypes(); } });
}
