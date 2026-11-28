// The check trace view: the evaluation graph (tracegraph.js) and its replay
// (traceplayer.js), the highlighted granting tuples, the side panel (node
// details, text trace), exports, and keeping the view across visits. The
// server computes what to show (granting path, folds, node details); this
// module draws it and handles interaction.
import { $, $$, baseOpts, color, esc, fitCapped, pal, reducedMotion, scrollBehavior, session, stateKey, store, whenLoaded } from './core.js';
import { onEscape, onKey } from './shortcuts.js';
import { applyFolds, expandFold, reveal, traceLayout, traceStyle } from './tracegraph.js';
import { PathFlow, Player } from './traceplayer.js';
import { initResizer, tabsOf } from './ui.js';

/**
 * @typedef {import('./tracegraph.js').Fold} Fold
 * @typedef {import('./tracegraph.js').GraphData} GraphData
 */

/**
 * Highlights (or clears) the tuples on the granting paths: the granting
 * tuples (edges the server marked "grant", which reach the user) in bold, the
 * other tuples on the paths labeled; marks their text trace lines; lists
 * them in the details; and brings them into view.
 * @param {FgaTrace} t @param {HTMLButtonElement} btn
 */
function toggleGrants(t, btn) {
  const { cy, tree, player: { flow } } = /** @type {TraceView} */ (t.view);
  const on = btn.getAttribute('aria-pressed') !== 'true';
  btn.setAttribute('aria-pressed', String(on));
  const grants = cy.edges('.grant');
  const others = cy.edges('.flowpath').filter((/** @type {any} */ e) => e.data('tuple') && !e.hasClass('grant'));
  cy.batch(() => { grants.toggleClass('grant-on', on); others.toggleClass('tuple-on', on); });
  const lineIds = (/** @type {any} */ edges) => new Set(edges.map((/** @type {any} */ e) => e.target().id()));
  const g = lineIds(grants);
  const o = lineIds(others);
  for (const l of $$('.tl', tree)) {
    l.classList.toggle('grant-line', on && g.has(l.dataset.node));
    l.classList.toggle('tuple-line', on && !g.has(l.dataset.node) && o.has(l.dataset.node));
  }
  const box = $('#node-detail', t);
  if (!on) { if (box && $('.grant-list', box)) clearDetail(t); return; }
  $('.tl.grant-line', tree)?.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() });
  // Bring the granting tuples into view (the list finds the others).
  const shown = grants.union(grants.connectedNodes());
  flow.userView = true;
  cy.stop();
  cy.animate({ fit: { eles: shown, padding: 80 }, duration: reducedMotion() ? 0 : 300,
    complete: () => { if (cy.zoom() > 1.2) { cy.zoom(1.2); cy.center(shown); } } });
  if (!box) return;
  /** @param {any} edges @returns {string} */
  const list = (edges) => {
    /** @type {Map<string, string>} tuple → edge id */
    const m = new Map();
    edges.forEach((/** @type {any} */ e) => { if (!m.has(e.data('tuple'))) m.set(e.data('tuple'), e.id()); });
    return `<ul class="grant-list">${[...m].map(([tu, id]) => `<li><button type="button" class="linkish" data-edge="${esc(id)}"><code>${esc(tu)}</code></button></li>`).join('')}</ul>`;
  };
  const n = new Set(grants.map((/** @type {any} */ e) => e.data('tuple'))).size;
  box.innerHTML = `<button type="button" class="close" aria-label="Clear the selection">×</button>
    <h3 id="node-detail-title">${n} granting tuple${n === 1 ? '' : 's'}</h3>
    <p class="muted sm">The tuples that reach the user at the end of a granting path. Select one to find it in the graph.</p>
    ${list(grants)}
    ${others.length ? `<h4 class="sm">Also on the granting paths</h4>
    <p class="muted sm">Tuples the paths go through on the way (parents, group memberships, …).</p>${list(others)}` : ''}`;
  togglePanel(t, false);
}

/** Shows the server-computed detail for a node. @param {FgaTrace} t @param {any} node */
function showDetail(t, node) {
  const box = $('#node-detail', t);
  const d = node.data('detail');
  if (!box || !d) return;
  /** @param {any} l */
  const link = (l) => (l.act
    ? `<button type="button" class="btn sm" data-act="${esc(l.act)}" data-order="${esc(l.order)}">${esc(l.text)}</button>`
    : l.href ? `<a class="btn sm${l.ghost ? ' ghost' : ''}" href="${esc(l.href)}">${esc(l.text)}</a>` : `<span class="muted">${esc(l.text)}</span>`);
  const rows = (d.rows || []).map((/** @type {any} */ r) => `<dt>${esc(r.key)}</dt><dd>${r.items
    ? `<ul class="checks">${r.items.map((/** @type {any} */ i) => `<li>${i.href ? `<a href="${esc(i.href)}">${esc(i.text)}</a>` : esc(i.text)}${i.note ? ` <span class="muted">${esc(i.note)}</span>` : ''}</li>`).join('')}</ul>`
    : esc(r.value)}</dd>`).join('');
  box.innerHTML = `<button type="button" class="close" aria-label="Clear the selection">×</button>
    <h3 id="node-detail-title">${esc(d.title)}</h3><dl>${rows}</dl><div class="links">${(d.links || []).map(link).join(' ')}</div>`;
  // Selecting a node is a request to see its details: open the panel.
  togglePanel(t, false);
}

const EMPTY_DETAIL = '<p class="nd-empty muted sm">Select a node in the graph or a line of the text trace to see what it means and where to go next.</p>';

/** Clears the selection and its details. @param {FgaTrace} t */
function clearDetail(t) {
  const box = $('#node-detail', t);
  if (box && !$('.nd-empty', box)) box.innerHTML = EMPTY_DETAIL;
  t.view?.cy.elements().unselect();
  $$('.tl.sel', t).forEach((l) => l.classList.remove('sel'));
}

/** @param {FgaTrace} t @param {HTMLButtonElement} btn */
function copyMarkdown(t, btn) {
  const v = $('.verdict', t);
  // The steps as numbered from the user up to the object, whatever reading is shown.
  const facts = $$('.facts ol.steps[data-reading="up"] > li.step', v).map((li) => `${li.classList.contains('reason') ? '✗ ' : ''}${$('.ft', li)?.textContent.trim() || ''}`);
  const md = [
    `**${$('.big', v)?.textContent.trim() || ''}**: ${$('.headline', v)?.textContent.trim() || ''}`,
    ...(facts.length ? ['', 'Why:', ...facts.map((f, i) => `${i + 1}. ${f}`)] : []),
    '', '```text', $('#trace-plain', t)?.textContent.trim() || '', '```',
    '', `Permalink: ${new URL($('.permalink', v)?.getAttribute('href') || '', location.href)}`,
  ].join('\n');
  navigator.clipboard?.writeText(md).then(() => {
    const old = btn.textContent;
    btn.textContent = 'Copied';
    setTimeout(() => { btn.textContent = old; }, 1200);
  });
}

/** @param {FgaTrace} t @param {any} cy */
function savePNG(t, cy) {
  const blob = cy.png({ output: 'blob', full: true, scale: 2, bg: color('--panel') });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = `trace-${($('.verdict', t)?.dataset.user || 'user').replace(/[^\w.-]+/g, '_')}.png`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 2000);
}

/** @typedef {{ cy: any, player: Player, tree: Element, folds: Fold[], sig: string }} TraceView */

/** Fits the whole graph; the view is the person's from then on. @param {FgaTrace} t */
const fitTrace = (t) => { if (t.view) { t.view.player.flow.userView = true; fitCapped(t.view.cy, undefined, 24, 1.3); } };

/**
 * Collapses or opens the side panel (details, text trace, who else).
 * @param {FgaTrace} t
 * @param {boolean} [collapse] collapse (true) or open (false); toggles when omitted
 */
function togglePanel(t, collapse) {
  const tv = $('.trace-views', t);
  if (!tv) return;
  const was = tv.classList.contains('panel-collapsed');
  const now = collapse ?? !was;
  tv.classList.toggle('panel-collapsed', now);
  for (const b of $$('[data-act="toggle-panel"]', t)) b.setAttribute('aria-expanded', String(!now));
  store.set('panelCollapsed', now);
  if (t.view && now !== was) resizeGraph(t.view);
}

/**
 * Resizes the graph to its box. A view the person set keeps its zoom and the
 * point at its center; otherwise the graph is framed (or fitted) again.
 * @param {TraceView} v
 */
function resizeGraph(v) {
  const { cy } = v;
  const { flow } = v.player;
  if (!flow.userView) {
    cy.resize();
    if (flow.started) flow.frame(); else fitCapped(cy, undefined, 24, 1.3);
    return;
  }
  const e = cy.extent();
  const mid = { x: (e.x1 + e.x2) / 2, y: (e.y1 + e.y2) / 2 };
  cy.resize();
  const z = cy.zoom();
  cy.pan({ x: cy.width() / 2 - mid.x * z, y: cy.height() / 2 - mid.y * z });
}

/**
 * Where the trace views' sizes (--trace-h, --text-w) are set: the result
 * container, which a re-run keeps while swapping its content, so the new views
 * take the old sizes in their first frame instead of jumping until this script
 * runs. The views themselves when there is no such container.
 * @param {HTMLElement} tv
 */
const sizeHolder = (tv) => /** @type {HTMLElement} */ (tv.closest('#check-result') || tv);

/** Sizes the check view to reach the bottom of the window on wide screens (CSS applies it). @param {FgaTrace} t */
function sizeTraceViews(t) {
  const tv = /** @type {HTMLElement | null} */ ($('.trace-views', t));
  if (!tv) return;
  const top = tv.getBoundingClientRect().top + window.scrollY;
  const h = `${Math.max(620, Math.floor(window.innerHeight - top - 16))}px`;
  const holder = sizeHolder(tv);
  if (holder.style.getPropertyValue('--trace-h') !== h) holder.style.setProperty('--trace-h', h);
}

/** Selects a node from the text trace or the graph. @param {FgaTrace} t @param {any} n @param {{ zoom?: boolean }} [opts] */
function selectNode(t, n, { zoom = false } = {}) {
  const v = /** @type {TraceView} */ (t.view);
  const { cy } = v;
  v.player.flow.userView = true;
  if (n.style('display') === 'none' && reveal(cy, n.id())) traceLayout(cy);
  cy.elements().unselect();
  n.select();
  cy.animate({ center: { eles: n }, ...(zoom ? { zoom: Math.max(cy.zoom(), 0.9) } : {}), duration: reducedMotion() ? 0 : 220 });
  showDetail(t, n);
}

/** Text trace as a tree: ↑/↓/Home/End move, Enter shows details. @param {FgaTrace} t */
function initTreeKeys(t) {
  const { tree, cy } = /** @type {TraceView} */ (t.view);
  const lines = $$('.tl', tree);
  if (!lines.length) return;
  lines[0].tabIndex = 0;
  tree.addEventListener('keydown', (/** @type {any} */ e) => {
    const cur = e.target.closest('.tl');
    if (!cur) return;
    const i = lines.indexOf(cur);
    /** @type {Record<string, number>} */
    const moves = { ArrowDown: Math.min(lines.length - 1, i + 1), ArrowUp: Math.max(0, i - 1), Home: 0, End: lines.length - 1 };
    const next = lines[moves[e.key] ?? -1];
    if (next) {
      e.preventDefault();
      cur.tabIndex = -1; next.tabIndex = 0; next.focus();
      cy.elements('.hl').removeClass('hl');
      cy.getElementById(next.dataset.node).addClass('hl');
    }
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      const n = cy.getElementById(cur.dataset.node);
      if (n.length) selectNode(t, n);
    }
  });
}

// ---- keeping the view (zoom, pan, opened folds, selection) ----
// Leaving the Check page (for Model, say) and coming back re-runs the check;
// the view the person had is kept per tab and put back if the result is the same.

/**
 * The view kept for a result: the graph (zoom, pan, opened folds, selected
 * node) and, saved when leaving the page, what else was open: "Why", the
 * side panel tab, and the scroll position.
 * @typedef {{ sig: string, zoom: number, pan: { x: number, y: number }, open: string[], sel: string, why?: boolean, tab?: string, scroll?: number }} SavedView
 */

const viewKey = () => stateKey('checkView');

/**
 * Identifies a result: the same check with the same graph (a hash of its
 * JSON, so two graphs of equal size do not pass for each other).
 * @param {FgaTrace} t @param {string} graph
 */
const viewSig = (t, graph) => {
  let h = 0x811c9dc5; // FNV-1a, 32 bits
  for (let i = 0; i < graph.length; i++) h = Math.imul(h ^ graph.charCodeAt(i), 0x01000193);
  return `${$('.verdict', t)?.dataset.summary || ''}|${graph.length}|${(h >>> 0).toString(36)}`;
};

/** The graph part of the kept view. @param {TraceView} v @returns {SavedView} */
const graphView = ({ cy, folds, sig }) => ({
  sig, zoom: cy.zoom(), pan: cy.pan(), open: folds.map((f) => f.id).filter((id) => cy.getElementById(id).empty()),
  sel: cy.$('node:selected').id() || '',
});

/** Saves the graph view as it changes. @param {TraceView} v */
function keepView(v) {
  let timer = 0;
  const save = () => {
    clearTimeout(timer);
    timer = window.setTimeout(() => {
      if (v.cy.destroyed()) return;
      session.set(viewKey(), { ...session.get(viewKey()), ...graphView(v) });
    }, 150);
  };
  v.cy.on('viewport select unselect remove', save);
}

/**
 * Puts a kept view back: opens the same folds (before layout), then restores
 * zoom, pan and selection (after the final frame, replacing its fit).
 * @param {FgaTrace} t @param {any} cy @param {SavedView} v @param {'folds' | 'viewport'} phase
 */
function restoreView(t, cy, v, phase) {
  if (phase === 'folds') {
    for (const id of v.open) { const f = cy.getElementById(id); if (f.nonempty()) expandFold(cy, f); }
    return;
  }
  if (v.why) { const why = /** @type {HTMLDetailsElement | null} */ ($('.verdict .facts', t)); if (why) why.open = true; }
  if (v.tab) tabsOf($(`#rtab-${CSS.escape(v.tab)}`, t))?.select(v.tab);
  if (v.scroll) requestAnimationFrame(() => window.scrollTo({ top: v.scroll }));
  cy.stop();
  cy.viewport({ zoom: v.zoom, pan: v.pan });
  const n = v.sel ? cy.getElementById(v.sel) : null;
  if (n?.nonempty() && n.visible()) {
    n.select();
    $$(`.tl[data-node="${CSS.escape(v.sel)}"]`, t).forEach((l) => l.classList.add('sel'));
    if (!$('.trace-views', t)?.classList.contains('panel-collapsed')) showDetail(t, n);
  }
}

/**
 * <fga-trace>: a check's result as the server renders it. It draws itself
 * when it arrives, once htmx has put it in place, and releases its graph,
 * replay and window listeners when it is removed. Everything it touches is
 * inside it, so more than one can be on a page.
 *
 * Parts (rendered by the server):
 *   .verdict             the decision, its "Why" (.facts) and data-* texts
 *                        for the player's closing status
 *   .player              replay controls ([data-act]: play, step, final,
 *                        fit, speed, autoplay, focus, copy-md, png,
 *                        toggle-panel) and .player-status (a live region)
 *   #trace-canvas        where the graph is drawn
 *   .trace-views         graph and side panel; .splitter resizes them
 *   #inspector           the side panel: #node-detail, the text trace
 *                        (#rpanel-trace .tree, #trace-plain), its tabs
 *   #trace-data          the graph as JSON (GraphData)
 *
 * Methods: play(), step(), back(), clearDetail(), togglePanel(), fit().
 *
 * Events: before drawing, it sends "fgax:trace" (detail: { restore: false }).
 * The page sets detail.restore when the result is a check being restored
 * (coming back to the page), so the kept view comes back instead of a replay.
 */
class FgaTrace extends HTMLElement {
  constructor() {
    super();
    /** @type {TraceView | null} */
    this.view = null;
    this.started = false;
    this.resizeTimer = 0;
    // Resize when the window changes, or when what sits above the graph does
    // (the form collapsing to its summary, “Why” opening).
    this.onResize = () => {
      clearTimeout(this.resizeTimer);
      this.resizeTimer = window.setTimeout(() => {
        sizeTraceViews(this);
        const v = this.view;
        if (!v || v.cy.destroyed()) return;
        const box = v.cy.container();
        if (box && (v.cy.width() !== box.clientWidth || v.cy.height() !== box.clientHeight)) resizeGraph(v);
      }, 120);
    };
    // Leaving the page (following a link to Explore, say) keeps what else
    // was open with the graph view, so coming back shows the result as it
    // was left.
    this.onPageHide = () => {
      if (!this.view || this.view.cy.destroyed()) return;
      const why = /** @type {HTMLDetailsElement | null} */ ($('.verdict .facts', this));
      session.set(viewKey(), {
        ...graphView(this.view),
        why: !!why?.open,
        tab: $('#inspector [role="tab"][aria-selected="true"]', this)?.dataset.tab || '',
        scroll: Math.round(window.scrollY),
      });
    };
    // Also watch the graph's own box, which the layout settles after drawing.
    this.observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(this.onResize);
  }

  connectedCallback() {
    if (this.started) return;
    this.started = true;
    window.addEventListener('resize', this.onResize);
    window.addEventListener('pagehide', this.onPageHide);
    whenLoaded(this, () => {
      if (!this.isConnected) return;
      const detail = { restore: false };
      this.dispatchEvent(new CustomEvent('fgax:trace', { bubbles: true, detail }));
      renderTrace(this, { replay: !detail.restore, restore: detail.restore });
    });
  }

  disconnectedCallback() {
    window.removeEventListener('resize', this.onResize);
    window.removeEventListener('pagehide', this.onPageHide);
    clearTimeout(this.resizeTimer);
    this.observer?.disconnect();
    this.view?.player.pause();
    this.view?.player.flow.stop();
    // Cytoscape keeps window listeners per instance: release the old graph.
    this.view?.cy.destroy();
    this.view = null;
  }

  play() { if (!this.view) return false; this.view.player.play(); return true; }
  step() { if (!this.view) return false; this.view.player.pause(); this.view.player.step(); return true; }
  back() { if (!this.view) return false; this.view.player.pause(); this.view.player.back(); return true; }
  clearDetail() { clearDetail(this); }
  togglePanel() { if (!$('.trace-views', this)) return false; togglePanel(this); return true; }
  fit() { fitTrace(this); }
}

/**
 * Draws the trace.
 * @param {FgaTrace} t
 * @param {{ replay?: boolean, restore?: boolean }} [opts] replay: animate the walk if the viewer allows it;
 *   restore: put back the view kept for this result (coming back to the page)
 */
function renderTrace(t, { replay = true, restore = false } = {}) {
  const dataEl = $('#trace-data', t);
  const container = $('#trace-canvas', t);
  const tree = $('#rpanel-trace .tree', t);
  if (!dataEl || !container || !tree) return;
  /** @type {GraphData} */
  const data = JSON.parse(dataEl.textContent || '{}');
  const sig = viewSig(t, dataEl.textContent || '');
  /** @type {SavedView | null} */
  const saved = restore ? session.get(viewKey()) : null;
  const kept = saved?.sig === sig ? saved : null;
  t.view?.player.flow.stop();
  sizeTraceViews(t);
  if (t.observer) { t.observer.disconnect(); t.observer.observe(document.body); t.observer.observe(container); }
  const cy = cytoscape({ container, elements: data.elements, style: traceStyle(pal()), layout: { name: 'null' }, ...baseOpts });
  applyFolds(cy, data.folds || []);
  if (kept) restoreView(t, cy, kept, 'folds');
  traceLayout(cy);
  fitCapped(cy, undefined, 24, 1.3);
  // Semantic zoom: short, larger labels when zoomed out.
  const farZoom = () => cy.elements().toggleClass('far', cy.zoom() < 0.55);
  cy.on('zoom', farZoom);
  farZoom();
  const flow = new PathFlow(cy, tree);
  cy.on('scrollzoom pinchzoom dragpan', () => { flow.userView = true; });
  const player = new Player(cy, data.steps, tree, flow, t);
  const view = { cy, player, tree, folds: data.folds || [], sig };
  t.view = view;

  const focus = /** @type {HTMLInputElement | null} */ ($('.player [data-act="focus"]', t));
  if (focus) {
    focus.checked = store.get('focusPath', true);
    flow.focus = focus.checked;
    focus.closest('label')?.classList.toggle('hidden', !flow.active);
    focus.addEventListener('change', () => {
      store.set('focusPath', focus.checked);
      flow.focus = focus.checked;
      if (player.i >= player.steps.length) {
        if (focus.checked) flow.start(); else { flow.stop(); fitTrace(t); }
      }
      player.update();
    });
  }
  const speed = /** @type {HTMLInputElement | null} */ ($('.player [data-act="speed"]', t));
  const auto = /** @type {HTMLInputElement | null} */ ($('.player [data-act="autoplay"]', t));
  if (speed && auto) {
    // Replaying is motion: off by default when the viewer asks for less.
    auto.checked = store.get('autoplay', !reducedMotion());
    speed.value = store.get('speed', 6);
    player.speed = +speed.value;
    if (replay && auto.checked) player.play(); else player.final();
    speed.addEventListener('input', () => { player.speed = +speed.value; store.set('speed', player.speed); });
    auto.addEventListener('change', () => store.set('autoplay', auto.checked));
  } else {
    player.final(); // no player bar: show the result as it ends
  }

  $('.player', t)?.addEventListener('click', (/** @type {any} */ e) => {
    switch (e.target.closest('[data-act]')?.dataset.act) {
      case 'play': player.play(); break;
      case 'step': player.pause(); player.step(); break;
      case 'final': player.final(); break;
      case 'fit': fitTrace(t); break;
      case 'copy-md': copyMarkdown(t, e.target.closest('button')); break;
      case 'png': savePNG(t, cy); break;
      case 'toggle-panel': togglePanel(t); break;
      default:
    }
  });

  cy.on('tap', 'node.fold', (/** @type {any} */ e) => { expandFold(cy, e.target); traceLayout(cy); });
  cy.on('tap', 'node', (/** @type {any} */ e) => {
    if (e.target.hasClass('fold')) return;
    showDetail(t, e.target);
    $$('.tl.sel', tree).forEach((l) => l.classList.remove('sel'));
    const lines = $$(`.tl[data-node="${CSS.escape(e.target.id())}"]`, tree);
    lines.forEach((l) => l.classList.add('sel'));
    lines[0]?.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() });
  });
  cy.on('tap', (/** @type {any} */ e) => { if (e.target === cy) clearDetail(t); });
  tree.addEventListener('mouseover', (/** @type {any} */ e) => {
    const l = e.target.closest('.tl');
    cy.elements('.hl').removeClass('hl');
    if (l) cy.getElementById(l.dataset.node).addClass('hl');
  });
  tree.addEventListener('mouseleave', () => cy.elements('.hl').removeClass('hl'));
  tree.addEventListener('click', (/** @type {any} */ e) => {
    const l = e.target.closest('.tl');
    const n = l && cy.getElementById(l.dataset.node);
    if (n?.length) selectNode(t, n, { zoom: true });
  });
  $('#node-detail', t)?.addEventListener('click', (/** @type {any} */ e) => {
    if (e.target.closest('.close')) clearDetail(t);
    // A granting tuple from the list: find it in the graph.
    const item = e.target.closest('[data-edge]');
    const edge = item && cy.getElementById(item.dataset.edge);
    if (edge?.nonempty()) {
      flow.userView = true;
      cy.elements('.hl').removeClass('hl');
      edge.addClass('hl');
      cy.animate({ center: { eles: edge }, zoom: Math.max(cy.zoom(), 0.9), duration: reducedMotion() ? 0 : 220 });
    }
  });
  const grantsBtn = /** @type {HTMLButtonElement | null} */ ($('.verdict [data-act="show-grants"]', t));
  grantsBtn?.addEventListener('click', () => toggleGrants(t, grantsBtn));
  $('.inspector', t)?.addEventListener('click', (/** @type {any} */ e) => {
    if (e.target.closest('.insp-head [data-act="toggle-panel"]')) togglePanel(t);
  });
  $('.panel-rail', t)?.addEventListener('click', () => togglePanel(t, false));
  initTreeKeys(t);
  // On phones the panel is a bottom sheet: closed until asked for (or a node is selected).
  togglePanel(t, store.get('panelCollapsed', matchMedia('(max-width: 700px)').matches));
  const views = /** @type {HTMLElement | null} */ ($('.trace-views', t));
  const setTextW = (/** @type {number} */ w) => { if (views) sizeHolder(views).style.setProperty('--text-w', `${Math.round(Math.max(w, 260))}px`); return w; };
  initResizer($('.splitter', views || t), (x) => {
    const r = (views || t).getBoundingClientRect();
    return setTextW(Math.min(r.right - x, r.width * 0.7));
  }, setTextW, 'textWidth', () => cy.resize());
  // Last, so nothing above (panel width, panel state) refits the restored view.
  if (kept) { restoreView(t, cy, kept, 'viewport'); flow.userView = true; }
  keepView(view);
}

/**
 * Defines <fga-trace> and registers the trace's keyboard shortcuts. They act
 * on the trace shown (the page's first drawn one).
 */
export function initTrace() {
  if (!customElements.get('fga-trace')) customElements.define('fga-trace', FgaTrace);
  const shown = () => /** @type {FgaTrace | null} */ ($('fga-trace:defined'));
  onKey('play', () => shown()?.play() ?? false);
  onKey('step', () => shown()?.step() ?? false);
  onKey('back', () => shown()?.back() ?? false);
  // Only on the check page: Explore registers its own panel and fit keys.
  if ($('#check-form')) {
    onKey('toggleText', () => shown()?.togglePanel() ?? false);
    onKey('fit', () => { shown()?.fit(); });
  }
  onEscape(() => shown()?.clearDetail());
}
