// @ts-check
// The check trace's graph: how it is drawn (outcome marks, styles), folding
// failed branches, and its layout (the granting path straight down the
// middle). The server computes what to show; this module draws it.
import { dagre, labelHeight, labelWidth, mix, pal, reducedMotion } from './core.js';

/**
 * @typedef {{ id: string, parent: string, label: string, hidden: string[] }} Fold
 * @typedef {{ phase: string, node: string, from?: string }} Step
 * @typedef {{ elements: any[], steps: Step[], root: string, folds?: Fold[] }} GraphData
 */

// Node labels carry an outcome mark (✓ ✗ ? ·) so outcomes never depend on color.
/** @type {Record<string, string>} */
const MARK = { allowed: '✓', denied: '✗', unknown: '?', error: '!', skipped: '·' };
// Skipped branches with a server verdict show it, marked as not walked.
/** @type {Record<string, string>} */
const HINT = { allowed: '(✓)', denied: '(✗)' };

/** @param {any} e @param {boolean} [short] */
function markedLabel(e, short = false) {
  const d = e.data();
  const text = short ? (d.short || d.label) : d.label;
  if (d.kind === 'subject' || d.kind === 'more' || d.kind === 'fold' || !MARK[d.outcome]) return text;
  if (d.kind === 'skipped' && HINT[d.hint]) return `${HINT[d.hint]} ${text}`;
  return `${MARK[d.outcome]} ${text}`;
}

/** @param {ReturnType<typeof pal>} p */
export function traceStyle(p) {
  /** @type {Record<string, string>} */
  const oc = { allowed: p.allowed, denied: p.denied, unknown: p.unknown, error: p.error, skipped: p.skipped };
  const s = [
    { selector: 'node', style: {
      shape: 'round-rectangle', label: (/** @type {any} */ e) => markedLabel(e), 'text-wrap': 'wrap', 'font-size': 11, color: p.fg, 'text-valign': 'center',
      'background-color': p.panel, 'border-width': 2, 'border-color': p.line,
      width: (/** @type {any} */ e) => labelWidth(e.data('label')), height: (/** @type {any} */ e) => labelHeight(e.data('label')),
      'transition-property': 'opacity, background-color, border-color', 'transition-duration': reducedMotion() ? '0ms' : '180ms',
    } },
    { selector: 'node.union, node.intersection, node.exclusion', style: { shape: 'ellipse', 'font-size': 12, width: 64, height: 34 } },
    { selector: 'node.direct', style: { shape: 'tag', 'font-size': 10 } },
    { selector: 'node.ttu', style: { shape: 'cut-rectangle', 'font-size': 10 } },
    { selector: 'node.subject', style: { shape: 'round-diamond', 'background-color': p.accent, color: '#fff', 'font-weight': 700, height: 44, width: (/** @type {any} */ e) => labelWidth(e.data('label')) + 30 } },
    { selector: 'node.blocked', style: { shape: 'octagon' } },
    { selector: 'node.cycle', style: { 'border-style': 'double', 'border-width': 4 } },
    { selector: 'node.more', style: { 'border-style': 'dashed', color: p.muted, 'font-style': 'italic', 'font-size': 10 } },
    // De-emphasis never uses opacity on labels: faded text drops below
    // readable contrast (and multiplies with .dim). Skipped nodes are dashed,
    // italic, on the page background, in solid muted colors.
    { selector: 'node.skipped', style: { 'border-style': 'dashed', 'background-color': p.bg, color: p.muted, 'font-style': 'italic' } },
    { selector: 'node.root', style: { 'border-width': 4, 'font-weight': 700 } },
    { selector: 'edge', style: {
      'curve-style': 'bezier', 'target-arrow-shape': 'triangle', width: 1.6, 'line-color': p.line, 'target-arrow-color': p.line,
      label: 'data(label)', 'font-size': 9, color: p.muted, 'text-background-color': p.bg, 'text-background-opacity': 0.9,
      'text-background-padding': 2, 'text-wrap': 'ellipsis', 'text-max-width': 200,
      'transition-property': 'opacity, line-color', 'transition-duration': reducedMotion() ? '0ms' : '180ms',
    } },
    { selector: 'edge.skipped', style: { 'line-style': 'dashed', width: 1.2 } },
    // Failed edges are dashed so allowed/denied never relies on color alone.
    { selector: 'edge.denied, edge.error', style: { 'line-style': 'dashed', 'line-dash-pattern': [6, 4] } },
    { selector: 'node.fold', style: {
      shape: 'round-rectangle', 'border-style': 'dashed', 'border-color': p.denied, color: p.muted, 'font-style': 'italic',
      'background-color': p.panel, 'font-size': 10.5, cursor: 'pointer',
    } },
    { selector: 'edge.fold', style: { 'line-style': 'dashed', 'line-color': p.denied, 'target-arrow-color': p.denied, opacity: 0.7 } },
    // Zoomed out: short labels that stay legible.
    { selector: 'node.far', style: { label: (/** @type {any} */ e) => markedLabel(e, true), 'font-size': 18, 'font-weight': 700 } },
    { selector: 'edge.far', style: { label: '' } },
    { selector: 'edge.blocking', style: { width: 3.5, 'line-style': 'dashed' } },
    { selector: 'edge.probe', style: { 'line-style': 'dotted', width: 2.5 } },
    { selector: 'edge.more', style: { 'line-style': 'dotted', 'target-arrow-shape': 'none' } },
  ];
  for (const [k, c] of Object.entries(oc)) {
    s.push({ selector: `node.${k}`, style: { 'border-color': c } });
    s.push({ selector: `edge.${k}`, style: { 'line-color': c, 'target-arrow-color': c } });
  }
  s.push(
    { selector: 'edge.blocking', style: { 'line-color': p.denied, 'target-arrow-color': p.denied, color: p.denied } },
    // Cytoscape accepts neither :not() nor color-mix(): tint with a data selector and a precomputed blend.
    { selector: 'node.allowed[kind != "subject"]', style: { 'background-color': mix(p.allowed, p.panel, 0.14) } },
    { selector: 'node.denied[kind != "subject"]', style: { 'background-color': mix(p.denied, p.panel, 0.12) } },
    { selector: 'node.mismatch', style: { 'border-color': p.mismatch, 'border-width': 5 } },
    { selector: '.pending', style: { opacity: 0.1 } },
    // Off the granting path while it is highlighted: shapes recede, labels stay legible.
    // Recedes by style, not opacity: a thin border (still 3:1), a lighter fill, muted label.
    { selector: 'node.dim', style: { 'border-width': 1, 'background-opacity': 0.5, color: p.muted } },
    { selector: 'edge.dim', style: { 'line-color': p.skipped, 'target-arrow-color': p.skipped, width: 1.2, color: p.muted } },
    // Skipped nodes are already de-emphasized (dashed, italic): don't fade them twice.
    { selector: 'node.skipped.dim', style: { 'border-opacity': 1, 'background-opacity': 1 } },
    { selector: 'edge.flow', style: {
      'line-style': 'dashed', 'line-dash-pattern': [10, 6], width: 3.6, 'line-color': p.allowed, 'target-arrow-color': p.allowed,
      'arrow-scale': 1.25, color: p.allowed, 'z-index': 20, opacity: 1,
    } },
    { selector: 'node.flow', style: {
      'border-width': 3, 'border-color': p.allowed, 'z-index': 20, opacity: 1,
      'underlay-color': p.allowed, 'underlay-padding': 8, 'underlay-opacity': 0, 'underlay-shape': 'round-rectangle',
    } },
    // "N granting tuples" pressed: the tuples that grant access, labeled, on every path.
    { selector: 'edge.grant-on', style: {
      'line-style': 'solid', width: 6, 'line-color': p.allowed, 'target-arrow-color': p.allowed, 'arrow-scale': 1.3,
      label: 'data(tuple)', 'font-size': 10, 'font-weight': 700, color: p.fg, 'text-background-color': p.bg, 'text-background-opacity': 1,
      'text-border-width': 1, 'text-border-color': p.allowed, 'text-border-opacity': 1, 'text-max-width': 360, 'z-index': 40,
    } },
    { selector: 'edge.tuple-on', style: {
      label: 'data(tuple)', 'font-size': 9.5, color: p.fg, 'text-background-color': p.bg, 'text-background-opacity': 1, 'text-max-width': 360, 'z-index': 30,
    } },
    { selector: 'node.visiting', style: { 'overlay-color': p.accent, 'overlay-opacity': 0.25, 'overlay-padding': 8, opacity: 1 } },
    { selector: 'node:selected', style: { 'overlay-color': p.accent, 'overlay-opacity': 0.18, 'overlay-padding': 6 } },
    { selector: '.hl', style: { 'overlay-color': p.accent, 'overlay-opacity': 0.22, 'overlay-padding': 6 } },
    // A "Why" step pointed at or pinned: its nodes and the edges between them.
    { selector: 'node.step-on', style: { 'border-color': p.accent, 'border-width': 4, 'underlay-color': p.accent, 'underlay-opacity': 0.25, 'underlay-padding': 7, 'z-index': 50 } },
    // Everything else while a step is lit: shapes recede, labels stay legible (as .dim).
    { selector: 'node.step-dim', style: { 'border-width': 1, 'background-opacity': 0.45, color: p.muted } },
    { selector: 'edge.step-dim', style: { 'line-color': p.skipped, 'target-arrow-color': p.skipped, width: 1.1, color: p.muted } },
    { selector: 'edge.step-on', style: { 'line-color': p.accent, 'target-arrow-color': p.accent, 'line-style': 'solid', width: 5, 'arrow-scale': 1.3, 'z-index': 50 } },
  );
  return s;
}

// ---- folds (computed by the server, see trace.Fold) ----

/** @param {any} cy @param {Fold[]} folds */
export function applyFolds(cy, folds) {
  cy.batch(() => {
    for (const f of folds) {
      cy.add([
        { group: 'nodes', data: { id: f.id, kind: 'fold', label: f.label, hidden: f.hidden }, classes: 'fold' },
        { group: 'edges', data: { id: `${f.id}-e`, source: f.parent, target: f.id, label: '' }, classes: 'fold' },
      ]);
    }
    for (const f of folds) for (const id of f.hidden) cy.getElementById(id).style('display', 'none');
  });
}

/** @param {any} cy @param {any} fold */
export function expandFold(cy, fold) {
  cy.batch(() => {
    for (const id of fold.data('hidden') || []) cy.getElementById(id).style('display', 'element');
    fold.remove();
  });
}

/** Expands whatever folds hide the element (outermost first). @param {any} cy @param {string} id */
export function reveal(cy, id) {
  const fold = cy.nodes('.fold').filter((/** @type {any} */ f) => (f.data('hidden') || []).includes(id))[0];
  if (!fold) return false;
  if (fold.style('display') === 'none') reveal(cy, fold.id());
  expandFold(cy, fold);
  return true;
}

/** The general layout: granting-path edges weigh heavily so dagre keeps the path short. @param {any} cy */
const dagreLayout = (cy) => cy.elements(':visible').layout(dagre('TB', {
  nodeSep: 18, rankSep: 44, fit: false,
  edgeWeight: (/** @type {any} */ e) => (e.hasClass('flowpath') ? 20 : 1),
})).run();

/**
 * The spine of the granting path: the longest chain of granting edges from
 * the root, or null when nothing was granted. Other granting branches (e.g.
 * the second side of an "and") are laid out beside it.
 * @param {any} cy
 */
function grantingChain(cy) {
  const root = cy.nodes('.root.flowpath:visible');
  if (!root.length) return null;
  /** @type {Map<string, number>} */
  const longest = new Map();
  /** @param {any} n @param {Set<string>} seen */
  const depth = (n, seen) => {
    if (longest.has(n.id())) return longest.get(n.id()) || 0;
    if (seen.has(n.id())) return 0;
    seen.add(n.id());
    let best = 0;
    n.outgoers('edge.flowpath').targets(':visible').forEach((/** @type {any} */ c) => { best = Math.max(best, depth(c, seen)); });
    longest.set(n.id(), best + 1);
    return best + 1;
  };
  depth(root[0], new Set());
  const chain = [root[0]];
  for (let n = root[0]; ;) {
    let next = null;
    n.outgoers('edge.flowpath').targets(':visible').forEach((/** @type {any} */ c) => {
      if (!chain.includes(c) && (!next || (longest.get(c.id()) || 0) > (longest.get(next.id()) || 0))) next = c;
    });
    if (!next) break;
    chain.push(next);
    n = next;
  }
  return chain.length > 1 ? chain : null;
}

/**
 * Lays the trace out with the granting path as a straight vertical spine
 * (object at the top, user at the bottom); each spine node's other branches
 * (including other granting routes) are laid out beside it, alternating
 * sides. Falls back to dagre when nothing was granted.
 * @param {any} cy
 */
export function traceLayout(cy) {
  const chain = grantingChain(cy);
  if (!chain) { dagreLayout(cy); return; }
  const onChain = new Set(chain.map((n) => n.id()));
  const placed = new Set(onChain);
  const gap = 44, side = 70;
  let y = 0;
  let right = true;
  cy.batch(() => {
    for (const p of chain) {
      // The branches hanging off this path node (not yet placed elsewhere).
      let block = cy.collection();
      const queue = [p];
      while (queue.length) {
        const n = queue.shift();
        n.outgoers('node:visible').forEach((/** @type {any} */ c) => {
          if (placed.has(c.id())) return;
          placed.add(c.id());
          block = block.union(c);
          queue.push(c);
        });
      }
      const h = p.outerHeight();
      p.position({ x: 0, y: y + h / 2 });
      let slot = h;
      if (block.length) {
        const sub = block.union(block.edgesWith(block));
        sub.layout(dagre('TB', { nodeSep: 14, rankSep: 36, fit: false })).run();
        const bb = block.boundingBox();
        const dx = right ? side + p.outerWidth() / 2 - bb.x1 : -side - p.outerWidth() / 2 - bb.x2;
        const dy = y - bb.y1;
        block.forEach((/** @type {any} */ n) => { const pos = n.position(); n.position({ x: pos.x + dx, y: pos.y + dy }); });
        slot = Math.max(h, bb.h);
        right = !right;
      }
      y += slot + gap;
    }
    // Anything not reached from the path goes underneath.
    const rest = cy.nodes(':visible').filter((/** @type {any} */ n) => !placed.has(n.id()));
    if (rest.length) {
      rest.union(rest.edgesWith(rest)).layout(dagre('TB', { nodeSep: 18, rankSep: 44, fit: false })).run();
      const bb = rest.boundingBox();
      rest.forEach((/** @type {any} */ n) => { const pos = n.position(); n.position({ x: pos.x - bb.x1 - bb.w / 2, y: pos.y - bb.y1 + y + gap }); });
    }
  });
}

/** The least zoom the trace is framed at: labels stay readable (about 9px). */
export const READABLE_ZOOM = 0.8;

/**
 * Where to put the view to show eles: fitted when that is readable (zoom no
 * more than cap); otherwise at READABLE_ZOOM, with the top of eles (the
 * object, where the walk starts) at the top of the graph and the root
 * centered across, so the path reads down from there. "Fit all" shows
 * everything.
 * @param {any} cy @param {any} eles @param {number} [pad] @param {number} [cap]
 * @returns {{ zoom: number, pan: { x: number, y: number } }}
 */
export function readableView(cy, eles, pad = 24, cap = 1.3) {
  const bb = eles.boundingBox();
  const w = Math.max(1, cy.width() - 2 * pad);
  const h = Math.max(1, cy.height() - 2 * pad);
  const fit = Math.min(w / Math.max(1, bb.w), h / Math.max(1, bb.h));
  const zoom = Math.max(READABLE_ZOOM, Math.min(cap, fit));
  if (zoom === Math.min(cap, fit)) {
    // It fits: centered.
    return { zoom, pan: { x: cy.width() / 2 - ((bb.x1 + bb.x2) / 2) * zoom, y: cy.height() / 2 - ((bb.y1 + bb.y2) / 2) * zoom } };
  }
  const root = eles.nodes('.root');
  const x = root.length ? root.position('x') : (bb.x1 + bb.x2) / 2;
  // Across: the root in the middle, unless the whole width fits.
  const across = bb.w * zoom <= w ? (bb.x1 + bb.x2) / 2 : x;
  return { zoom, pan: { x: cy.width() / 2 - across * zoom, y: pad - bb.y1 * zoom } };
}

/** Shows eles at once, as readableView says. @param {any} cy @param {any} [eles] all elements when omitted */
export function frameReadable(cy, eles) {
  const v = readableView(cy, eles || cy.elements(':visible'));
  cy.viewport(v);
}
