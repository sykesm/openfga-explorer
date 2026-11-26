// The Explore view: the type graph, the instance graph built up as you open
// instances, the side panel (type and instance panels, rendered by the
// server), and the per-tab persistence of all of it.
import { $, $$, article, base, baseOpts, categoricalColor, dagre, fitCapped, labelWidth, load, pal, scrollBehavior, session, stateKey, store, typeColor } from './core.js';
import { initEdgeLists } from './edgelist.js';
import { picker } from './pickers.js';
import { onKey } from './shortcuts.js';
import { initTypeFinder } from './typefinder.js';
import { initResizer, tabsOf } from './ui.js';

/** @typedef {{ open(): void, remember(t: string): void }} TypeFinder <fga-type-finder> (typefinder.js) */
/** @returns {TypeFinder | null} */
const typeFinder = () => /** @type {any} */ ($('fga-type-finder:defined'));

/** @typedef {import('./edgelist.js').FgaEdgeList} FgaEdgeList */

/** @type {any} */ let schemaCy = null;
/** @type {any} */ let instCy = null;
// Whether the type graph shows a neighborhood laid out on its own.
let subLayout = false;
// Large models start with an empty type graph (see exploreData.Large).
const isLarge = () => $('.explore-layout')?.dataset.large === 'true';
// The "too many types to draw" prompt shows (on the type graph tab) until a
// type is chosen or every type is drawn.
let promptWanted = false;
let noteText = '';
// The prompt and the note belong to the type graph tab.
const updatePrompt = () => {
  const onSchema = !$('#schema-canvas')?.hidden;
  const el = $('#schema-empty');
  if (el) el.hidden = !(promptWanted && onSchema);
  const note = $('#schema-note');
  if (note) note.hidden = !(noteText && onSchema);
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
let relayoutTimer = 0;

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
  const legend = $('.mod-legend');
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
  schemaCy?.style().update();
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

/** @param {ReturnType<typeof pal>} p */
function instStyle(p) {
  return [
    { selector: 'node', style: {
      shape: 'round-rectangle', label: 'data(label)', 'font-size': 11, color: p.fg, 'text-valign': 'center',
      'background-color': p.panel, 'border-width': 2, 'border-color': (/** @type {any} */ e) => typeColor(e.data('type')),
      width: (/** @type {any} */ e) => labelWidth(e.data('label')), height: 28, 'text-wrap': 'ellipsis', 'text-max-width': 260,
    } },
    { selector: 'node[?focus]', style: { 'border-width': 4 } },
    { selector: 'node.current', style: { 'background-color': (/** @type {any} */ e) => typeColor(e.data('type')), color: '#fff' } },
    { selector: 'node[kind = "count"]', style: {
      shape: 'round-tag', 'border-style': 'dashed', 'border-color': p.muted, color: p.muted, 'font-style': 'italic',
    } },
    { selector: 'edge', style: {
      'curve-style': 'bezier', 'target-arrow-shape': 'triangle', width: 1.4, 'line-color': p.line, 'target-arrow-color': p.line,
      label: 'data(label)', 'font-size': 9, color: p.muted, 'text-background-color': p.bg, 'text-background-opacity': 0.9, 'text-background-padding': 2,
    } },
    { selector: 'edge.count', style: { 'line-style': 'dashed' } },
  ];
}

/**
 * Collapses or opens the side panel (types, type and instance panels).
 * @param {boolean} [collapse] collapse (true) or open (false); toggles when omitted
 */
function toggleSide(collapse) {
  const layout = $('.explore-layout');
  if (!layout) return;
  const was = layout.classList.contains('side-collapsed');
  const now = collapse ?? !was;
  layout.classList.toggle('side-collapsed', now);
  for (const b of $$('[data-act="toggle-side"]')) b.setAttribute('aria-expanded', String(!now));
  store.set('sideCollapsed', now);
  if (now !== was) {
    resizeAll();
    fitCapped(activeCy());
  }
}

const activeCy = () => ($('#schema-canvas')?.hidden ? instCy : schemaCy);
const resizeAll = () => { instCy?.resize(); schemaCy?.resize(); };

/** Shows a graph (its tab is selected; canvasShown follows). @param {'schema' | 'instances'} which */
const showCanvas = (which) => tabsOf('#graph-tabs')?.select(which);

/**
 * Updates the rest of the view for the graph now showing.
 * @param {'schema' | 'instances'} which
 */
function canvasShown(which) {
  // The side panel follows the canvas: types on the type graph, the open
  // instance (kept, not discarded) on the instance graph.
  $('#explore-side')?.classList.toggle('on-schema', which === 'schema');
  setInstanceFocus(which === 'instances' && !!$('#instance-panel .instance'));
  $$('.canvas-wrap .legend').forEach((l) => l.classList.toggle('hidden', l.dataset.for !== which));
  const cy = which === 'schema' ? schemaCy : instCy;
  if (cy) { cy.resize(); fitCapped(cy); }
  updateEmpty();
  updatePrompt();
  saveExplore();
}

// ---- type graph ----

/**
 * Shows the selected type's neighborhood (two hops) in the type graph,
 * hiding the rest until "Show all types" is pressed.
 * @param {string} t
 */
function focusType(t) {
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
      const keep = neighbors.sort((a, b) => degree(b) - degree(a) || (a.id() < b.id() ? -1 : 1)).slice(0, MAX_NEIGHBORS);
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
  if (!$('#schema-canvas').hidden) fitCapped(schemaCy, schemaCy.elements(':visible'));
}

function showAllTypes() {
  schemaPrompt(false);
  schemaNote('');
  schemaCy.elements().style('display', 'element');
  if (subLayout) {
    subLayout = false;
    schemaCy.layout(dagre('LR', { rankSep: 150, nodeSep: 40, fit: false })).run();
  }
  $('.canvas-bar [data-act="show-all-types"]')?.classList.add('hidden');
  fitCapped(schemaCy);
}

/** Marks t as the selected type among the chips, and remembers it as recent. @param {string} t */
function markType(t) {
  $$('.type-chip').forEach((c) => c.classList.toggle('on', c.dataset.type === t));
  $(`.type-list .type-chip[data-type="${CSS.escape(t)}"]`)?.scrollIntoView({ block: 'nearest' });
  typeFinder()?.remember(t);
}

/**
 * @param {string} t
 * @param {{ loadPanel?: boolean, browse?: boolean }} [opts] browse: the person
 *   is choosing a type (not opening an instance), so the type section shows
 */
function selectType(t, { loadPanel = true, browse = true } = {}) {
  if (browse) setInstanceFocus(false);
  markType(t);
  if (schemaCy) focusType(t);
  const panel = $('#type-panel');
  if (loadPanel && panel && panel.querySelector('[data-selected-type]')?.dataset.selectedType !== t) {
    return load(`${base()}/explore/type?type=${encodeURIComponent(t)}`, panel).then(() => { updateEmpty(); saveExplore(); });
  }
  return Promise.resolve();
}

/**
 * With an instance open, the side panel shows just the instance: the type
 * section (type list, type panel and its picker) is hidden until asked for
 * ("Type …", "Pick another…") or a type is chosen.
 * @param {boolean} on
 */
function setInstanceFocus(on) {
  const side = $('#explore-side');
  if (!side) return;
  side.classList.toggle('focus-instance', on);
  for (const b of $$('[data-act="show-type"]', side)) b.setAttribute('aria-expanded', String(!on));
}

/** Opens an instance (or a userset's object) in the side panel. @param {string} obj */
function loadInstance(obj) {
  const i = obj.indexOf(':');
  if (i <= 0) return Promise.resolve();
  let id = obj.slice(i + 1);
  const hash = id.lastIndexOf('#');
  if (hash >= 0) { obj = obj.slice(0, i + 1 + hash); id = id.slice(0, hash); }
  const type = obj.slice(0, i);
  selectType(type, { browse: false });
  const url = new URL(location.href);
  url.searchParams.set('type', type); url.searchParams.set('id', id); url.searchParams.delete('relation');
  history.replaceState(null, '', url);
  const panel = $('#instance-panel');
  return panel ? load(`${base()}/explore/instance?object=${encodeURIComponent(obj)}`, panel).then(instanceShown) : Promise.resolve();
}

/** With an instance loaded, the side panel shows just it, scrolled into view. */
function instanceShown() {
  const panel = $('#instance-panel');
  if ($('.instance', panel)) setInstanceFocus(true);
  const det = $('#type-panel .type-details');
  if (det) det.open = false;
  panel.scrollIntoView({ block: 'start', behavior: scrollBehavior() });
  saveExplore();
}

// ---- instance graph ----

/** @param {any} cy */
function relayout(cy) {
  if (!cy || cy.elements().length === 0) return;
  cy.layout(dagre('LR', { fit: false, rankSep: 130, nodeSep: 18 })).run();
  fitCapped(cy);
}

/**
 * <fga-graph-patch>: server-computed additions to the instance graph (an
 * instance's edges, as JSON in a <script type="application/json"> child),
 * sent with each instance panel and Incoming tab. It adds them to the graph
 * when it arrives on the page, once.
 */
class FgaGraphPatch extends HTMLElement {
  connectedCallback() {
    const json = $('script', this)?.textContent;
    if (this.applied || !json) return;
    this.applied = true;
    applyPatch(JSON.parse(json));
  }
}

/** Adds server-computed elements (an instance's edges) to the instance graph. @param {any} patch */
function applyPatch(patch) {
  if (!instCy) return;
  let added = 0;
  instCy.batch(() => {
    for (const el of patch.elements || []) {
      if (instCy.getElementById(el.data.id).length) continue;
      if (el.group === 'edges' && (!instCy.getElementById(el.data.source).length || !instCy.getElementById(el.data.target).length)) continue;
      instCy.add(el); added++;
    }
    instCy.nodes().removeClass('current');
    if (patch.focus) instCy.getElementById(patch.focus).addClass('current');
  });
  updateCount();
  markEdgeLists();
  showCanvas('instances');
  if (added) relayout(instCy);
  const f = instCy.getElementById(patch.focus);
  if (f.length) instCy.animate({ center: { eles: f }, duration: 250 });
}

/**
 * An instance-graph edge: user (source) → object (target), and the object's
 * relation it stands for.
 * @typedef {{ source: string, target: string, label: string, relation?: string, sourceLabel?: string, targetLabel?: string }} EdgeData
 */

/** @param {EdgeData} d */
const edgeId = (d) => `${d.source}|${d.label}|${d.target}`;
/** @param {EdgeData} d */
const onGraph = (d) => !!instCy && instCy.getElementById(edgeId(d)).length > 0;

/** @param {EdgeData} d */
function addEdge(d) {
  if (!instCy || !d) return;
  instCy.batch(() => {
    for (const [nid, lbl] of [[d.source, d.sourceLabel], [d.target, d.targetLabel]]) {
      if (!instCy.getElementById(nid).length) instCy.add({ group: 'nodes', data: { id: nid, label: lbl || nid, type: String(nid).split(':')[0] } });
    }
    if (!instCy.getElementById(edgeId(d)).length) instCy.add({ group: 'edges', data: { id: edgeId(d), source: d.source, target: d.target, label: d.label, relation: d.relation } });
    const anchor = $('#instance-panel .instance')?.dataset.object;
    if (anchor) instCy.getElementById(anchor).addClass('current');
  });
}

/** @param {EdgeData} d */
function removeEdge(d) {
  const e = instCy.getElementById(edgeId(d));
  if (!e.length) return;
  const ends = e.connectedNodes();
  e.remove();
  // Drop peers left with no edges, but keep the instance being explored.
  const anchor = $('#instance-panel .instance')?.dataset.object;
  ends.forEach((/** @type {any} */ n) => { if (n.id() !== anchor && !n.hasClass('current') && n.connectedEdges().length === 0) n.remove(); });
}

function updateCount() {
  const c = $('#inst-count');
  if (c && instCy) c.textContent = instCy.nodes().filter((/** @type {any} */ n) => n.data('kind') !== 'count').length;
  updateEmpty();
}

/** The instance graph fills in from instances you open; an empty canvas says how. */
function updateEmpty() {
  const box = $('#instance-empty');
  if (!box || !instCy) return;
  const showing = !$('#instance-canvas').hidden;
  box.classList.toggle('hidden', !(showing && instCy.nodes().length === 0));
  const type = $('#type-panel [data-selected-type]')?.dataset.selectedType;
  const btn = $('[data-act="start-picking"]', box);
  const msg = $('.ce-msg', box);
  if (type) {
    msg.textContent = `Pick ${article(type)} ${type} instance to start. Its edges appear here, and clicking a node walks to it.`;
    btn.textContent = `Pick ${article(type)} ${type} instance`;
  } else {
    msg.textContent = 'It fills in as you open instances. Choose a type on the right, then pick one of its instances.';
    btn.textContent = 'Choose a type';
  }
}

function startPicking() {
  const p = picker('#type-panel');
  if (p) { p.open(); return; }
  const finder = typeFinder();
  if (finder) { finder.open(); return; }
  $$('.type-chip').forEach((c) => { c.classList.remove('nudge'); void c.offsetWidth; c.classList.add('nudge'); });
}

/** @param {{ show?: boolean }} [opts] */
function graphChanged({ show = false } = {}) {
  updateCount();
  markEdgeLists();
  saveExplore();
  if (show) showCanvas('instances');
  clearTimeout(relayoutTimer);
  relayoutTimer = window.setTimeout(() => relayout(instCy), 120);
}

/**
 * The edge lists (edgelist.js) mirror the graph: checked (or "−") means the
 * edge is on it. Lists not set up yet (:defined) mark themselves on arrival.
 */
function markEdgeLists() {
  for (const list of /** @type {FgaEdgeList[]} */ ($$('fga-edge-list:defined'))) list.mark(onGraph);
}

// ---- instance panel tabs (Edges / Derived / Incoming / Access) ----
// <fga-tabs> (ui.js) remembers the last tab per type and loads the lazy ones.

/**
 * Shows an instance panel tab, by name or by its panel.
 * @param {string | Element} which
 */
const showTab = (which) => tabsOf('#instance-panel .itabs')?.select(which) ?? Promise.resolve();

/** Opens the edge chooser behind a "100+" count node. @param {any} d */
function openChooser(d) {
  const tab = d.chooser.startsWith('in-') ? 'incoming' : 'edges';
  const go = () => showTab(tab).then(() => {
    const btn = $(`[data-chooser-btn="${CSS.escape(d.chooser)}"]`);
    if (!btn) return;
    const row = btn.closest('#instance-panel [id^="rel-"], #instance-panel tr');
    if (row) markRelation(row);
    btn.scrollIntoView({ block: 'center', behavior: scrollBehavior() });
    btn.click();
  });
  if ($('#instance-panel .instance')?.dataset.object === d.owner) go();
  else loadInstance(d.owner).then(go);
}

// ---- persistence (per browser tab) ----

function saveExplore() {
  if (!instCy || !$('#schema-canvas')) return;
  session.set(stateKey('explore'), {
    type: $('#type-panel [data-selected-type]')?.dataset.selectedType || '',
    object: $('#instance-panel .instance')?.dataset.object || '',
    canvas: $('#schema-canvas').hidden ? 'instances' : 'schema',
    elements: instCy.elements().jsons(),
  });
}

function restoreExplore() {
  const p = new URLSearchParams(location.search);
  if (p.get('type') || p.get('id')) return false; // an explicit link wins
  const saved = session.get(stateKey('explore'));
  if (!saved) return false;
  if (saved.elements?.length) { instCy.add(saved.elements); updateCount(); }
  const done = () => { showCanvas(saved.canvas || 'schema'); saveExplore(); };
  if (saved.object) loadInstance(saved.object).then(done);
  else { if (saved.type) selectType(saved.type); done(); }
  return true;
}

// ---- wiring ----

/**
 * Goes to relations in a panel (one row, or several from a type-graph edge):
 * switches to their tab or opens their section if needed, scrolls the first
 * into view only when it is out of view, marks them (until another is
 * chosen) and moves focus to the first, so it can be found in the list.
 * @param {...HTMLElement} targets
 */
function goToRelation(...targets) {
  const [first] = targets;
  if (!first) return;
  const sec = first.closest('.ipanel');
  const shown = sec instanceof HTMLElement && sec.hidden ? showTab(sec) : Promise.resolve();
  const det = first.closest('details');
  if (det) det.open = true;
  shown.then(() => {
    markRelation(...targets);
    for (const t of targets) {
      t.classList.remove('rel-arrive');
      void t.offsetWidth; // restart the arrival pulse
      t.classList.add('rel-arrive');
    }
    if (!first.hasAttribute('tabindex')) first.setAttribute('tabindex', '-1');
    first.focus({ preventScroll: true });
    first.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() });
  });
}

/**
 * Marks the chosen relation (one at a time, or the relations of one
 * type-graph edge): the target of a relation link or a graph edge, or the
 * row someone clicks or tabs into.
 * @param {...Element} els
 */
function markRelation(...els) {
  const marked = $$('.rel-target');
  if (marked.length === els.length && els.every((el) => marked.includes(el))) return;
  marked.forEach((x) => x.classList.remove('rel-target', 'rel-arrive'));
  els.forEach((el) => el.classList.add('rel-target'));
}

/**
 * An instance-graph edge stands for a relation on its target (the object):
 * tapping it opens that instance, as tapping the node does, and goes to the
 * relation. A count edge opens its edge chooser, as its count node does.
 * @param {any} edge a Cytoscape edge
 */
function tapInstanceEdge(edge) {
  const count = edge.connectedNodes('[kind = "count"]');
  if (count.length) { openChooser(count.data()); return; }
  const d = /** @type {EdgeData} */ (edge.data());
  const rel = d.relation || d.label.split(' ')[0]; // graphs saved before edges carried it
  const go = () => {
    const row = document.getElementById(`rel-${rel}`);
    if (row) goToRelation(row);
  };
  if ($('#instance-panel .instance')?.dataset.object === d.target) go();
  else loadInstance(d.target).then(go);
}

/**
 * A type-graph edge stands for relations on its target type (several when
 * parallel edges are merged): tapping it selects that type, as tapping the
 * node does, and goes to those relations in the type panel.
 * @param {any} edge a Cytoscape edge
 */
function tapTypeEdge(edge) {
  const d = edge.data();
  selectType(d.target).then(() => {
    const rows = (d.relations || []).map((/** @type {string} */ r) => document.getElementById(`type-rel-${r}`)).filter(Boolean);
    goToRelation(...rows);
  });
}

/** The relation row (Edges or Derived tab) holding an element. @param {EventTarget | null} t */
const relationOf = (t) => (t instanceof Element ? t.closest('#instance-panel [id^="rel-"]') : null);

/** @param {MouseEvent} e */
function onClick(e) {
  const t = /** @type {Element} */ (e.target);
  const row = relationOf(t);
  if (row) markRelation(row);
  const chip = t.closest('.type-chip');
  if (chip instanceof HTMLElement) { selectType(chip.dataset.type || ''); showCanvas('schema'); return; }
  if (t.closest('[data-act="start-picking"]')) toggleSide(false);
  if (t.closest('[data-act="start-picking"]')) { startPicking(); return; }
  // The instance header: show the type section, or pick another instance.
  if (t.closest('[data-act="show-type"]')) { setInstanceFocus(!$('#explore-side')?.classList.contains('focus-instance')); return; }
  if (t.closest('[data-act="change-instance"]')) {
    setInstanceFocus(false);
    const p = picker('#type-panel');
    if (p) { p.q.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() }); p.open(); }
    return;
  }
  // Links to a relation row (possibly in another tab): show and mark it.
  const anchor = t.closest('a.anchor[href^="#rel-"]');
  if (anchor) {
    const target = document.getElementById((anchor.getAttribute('href') || '').slice(1));
    if (target) { e.preventDefault(); goToRelation(target); }
    return;
  }
  if (t.closest('#schema-empty [data-act="show-all-types"]')) { showAllTypes(); return; }
  const act = t.closest('.canvas-bar [data-act]');
  if (act instanceof HTMLElement) {
    const cy = activeCy();
    switch (act.dataset.act) {
      case 'fit': fitCapped(cy); break;
      case 'relayout': relayout(cy); break;
      case 'clear': instCy.elements().remove(); graphChanged(); break;
      case 'reset-explore': session.del(stateKey('explore')); location.assign(`${base()}/explore`); break;
      case 'show-all-types': showAllTypes(); break;
      case 'toggle-side': toggleSide(); break;
      default:
    }
    return;
  }
  // Walks to a peer (an edge list has already added the edge to the graph).
  const peer = t.closest('[data-explore]');
  if (peer instanceof HTMLElement && $('#instance-canvas')) {
    e.preventDefault();
    loadInstance(peer.dataset.explore || '');
  }
}

export function initExplore() {
  const sc = $('#schema-canvas');
  if (!sc) return;
  document.addEventListener('fgax:type-chosen', (/** @type {any} */ e) => { selectType(e.detail.type); showCanvas('schema'); });
  initTypeFinder();
  toggleSide(store.get('sideCollapsed', false));
  $('.canvas-wrap > .panel-rail')?.addEventListener('click', () => toggleSide(false));
  const setSideW = (/** @type {number} */ w) => { document.documentElement.style.setProperty('--side-w', `${Math.round(Math.min(Math.max(w, 320), window.innerWidth * 0.7))}px`); return w; };
  initResizer($('.side-resizer'), (x) => setSideW(window.innerWidth - x), setSideW, 'sideWidth', resizeAll);
  const p = pal();
  instCy = cytoscape({ container: $('#instance-canvas'), elements: [], style: instStyle(p), ...baseOpts });
  if (!customElements.get('fga-graph-patch')) customElements.define('fga-graph-patch', FgaGraphPatch);
  instCy.on('tap', 'node', (/** @type {any} */ e) => {
    const d = e.target.data();
    // Opening something from the graph fills the panel: show it.
    toggleSide(false);
    if (d.kind === 'count') openChooser(d); else loadInstance(d.id);
  });
  instCy.on('tap', 'edge', (/** @type {any} */ e) => { toggleSide(false); tapInstanceEdge(e.target); });
  fetch(`${base()}/api/schema`).then((r) => r.json()).then((data) => {
    const large = isLarge();
    const toggle = $('[data-act="color-modules"]');
    if (toggle) {
      toggle.checked = store.get('colorByModule', false);
      byModule = toggle.checked;
      toggle.addEventListener('change', () => colorByModule(toggle.checked));
    }
    // A large model is not laid out (or shown) until asked: only neighborhoods.
    schemaCy = cytoscape({ container: sc, elements: data.elements, style: schemaStyle(p),
      layout: large ? { name: 'null' } : dagre('LR', { rankSep: 150, nodeSep: 40 }), ...baseOpts });
    if (large) {
      schemaCy.elements().style('display', 'none');
      subLayout = true;
      $('.canvas-bar [data-act="show-all-types"]')?.classList.remove('hidden');
      schemaPrompt(true);
    } else fitCapped(schemaCy);
    if (byModule) colorByModule(true);
    // Choosing a type shows its details; it never opens the instance picker
    // over them (only "Pick … instance" and "/" do).
    schemaCy.on('tap', 'node', (/** @type {any} */ e) => { toggleSide(false); selectType(e.target.id()); });
    schemaCy.on('tap', 'edge', (/** @type {any} */ e) => { toggleSide(false); tapTypeEdge(e.target); });
    // Tapping the background shows every type again, except in large models
    // (it would bring back the unreadable whole).
    schemaCy.on('tap', (/** @type {any} */ e) => { if (e.target === schemaCy && !large) { schemaCy.elements().removeClass('faded sel'); showAllTypes(); } });
    const tp = $('#type-panel');
    const ip = $('#instance-panel');
    if (restoreExplore()) return;
    if (ip.dataset.initialType && ip.dataset.initialId) {
      loadInstance(`${ip.dataset.initialType}:${ip.dataset.initialId}`).then(() => {
        const rel = ip.dataset.initialRelation;
        const target = rel && document.getElementById(`rel-${rel}`);
        if (target) goToRelation(target);
      });
    } else if (tp.dataset.initialType) {
      selectType(tp.dataset.initialType);
    }
  });

  document.addEventListener('click', onClick);
  document.addEventListener('fgax:tab', (/** @type {any} */ e) => { if (e.target.id === 'graph-tabs') canvasShown(e.detail.name); });
  document.addEventListener('focusin', (e) => { const row = relationOf(e.target); if (row) markRelation(row); });
  // Edge lists ask for edges to be added or removed, and to be marked.
  document.addEventListener('fgax:edges', (/** @type {any} */ e) => {
    const { edges, add } = e.detail;
    for (const d of edges) { if (add) addEdge(d); else removeEdge(d); }
    graphChanged({ show: add });
  });
  document.addEventListener('fgax:edge-list', (/** @type {any} */ e) => e.target.mark(onGraph));
  initEdgeLists();
  document.addEventListener('fgax:picked', (/** @type {any} */ e) => { if (e.detail.mode === 'explore') loadInstance(e.detail.value); });
  onKey('fit', () => fitCapped(activeCy()));
  onKey('toggleText', () => toggleSide());
  onKey('find', () => {
    const box = picker('#type-panel') ?? typeFinder();
    if (box) box.open(); else $('.type-chip')?.focus();
  });
}


