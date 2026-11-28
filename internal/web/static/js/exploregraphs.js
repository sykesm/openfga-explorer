// @ts-check
// What the Explore view's two graphs share: the graphs themselves (the type
// graph, schemagraph.js, and the instance graph, instancegraph.js), which one
// is showing, and fitting and laying them out.
import { $, dagre, fitCapped } from './core.js';
import { tabsOf } from './ui.js';

/**
 * The Explore view's graphs, set when they are drawn.
 * @type {{ schema: any, inst: any }}
 */
export const graphs = { schema: null, inst: null };

// Graphs fitted to a view at least once. After that, choosing or opening
// something keeps the person's zoom; only Fit, Re-layout, and Show all types
// fit the graph again.
const fitted = new WeakSet();
/** @param {any} cy @param {any} [eles] */
export const fitView = (cy, eles) => { fitCapped(cy, eles); fitted.add(cy); };
/** Whether a graph has been fitted to a view. @param {any} cy */
export const wasFitted = (cy) => fitted.has(cy);

/** Lays out a graph again, and fits it. @param {any} cy */
export function relayout(cy) {
  if (!cy || cy.elements().length === 0) return;
  cy.layout(dagre('LR', { fit: false, rankSep: 130, nodeSep: 18 })).run();
  fitView(cy);
}

/** The graph showing. */
export const activeCy = () => ($('#schema-canvas')?.hidden ? graphs.inst : graphs.schema);
export const resizeAll = () => { graphs.inst?.resize(); graphs.schema?.resize(); };

/** Shows a graph (its tab is selected; the page follows). @param {'schema' | 'instances'} which */
export const showCanvas = (which) => tabsOf('#graph-tabs')?.select(which);

/**
 * Shows or hides a note over a graph. The note moves the graph's canvas, so
 * the graph re-measures it: clicks would otherwise land a line off.
 * @param {HTMLElement} note @param {boolean} show @param {any} cy
 */
export function showNote(note, show, cy) {
  if (note.hidden === !show) return;
  note.hidden = !show;
  cy?.resize();
}

/** @param {number} k @param {string} one @param {string} [many] */
export const plural = (k, one, many = `${one}s`) => `${k} ${k === 1 ? one : many}`;
