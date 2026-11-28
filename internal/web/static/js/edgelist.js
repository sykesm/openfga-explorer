// @ts-check
// <fga-edge-list>: edges (tuples) that can be put on the instance graph,
// listed in the instance panel and in the edge chooser.
//
// The server renders the element around li[data-edge] rows (data-edge: the
// edge as JSON, see EdgeData in explore.js). A row has a ＋/− toggle
// (.add-edge) or a checkbox (.edge-check), and a .peer link. A chooser page
// adds .add-shown and .remove-shown buttons and a .sel-count line.
//
// The element knows nothing about the graph. It reports what the person asks
// for, and the page marks it:
//   fgax:edges      add or remove edges (detail: { edges, add }); a .peer
//                   link adds its edge (the page then walks to the peer)
//   fgax:edge-list  the list arrived: mark it
//   mark(isOn)      shows which edges are on the graph
import { $, $$ } from './core.js';

/** @param {Element} el @returns {any} the edge of the row holding el */
const edgeOf = (el) => JSON.parse(/** @type {HTMLElement} */ (el.closest('[data-edge]'))?.dataset.edge || 'null');

export class FgaEdgeList extends HTMLElement {
  connectedCallback() {
    if (!this.wired) {
      this.wired = true;
      this.addEventListener('click', (e) => this.onClick(e));
      this.addEventListener('change', (e) => {
        const cb = e.target;
        if (cb instanceof HTMLInputElement && cb.matches('.edge-check')) this.request([edgeOf(cb)], cb.checked);
      });
    }
    this.dispatchEvent(new CustomEvent('fgax:edge-list', { bubbles: true }));
  }

  /** @returns {HTMLElement[]} */
  get rows() { return $$('li[data-edge]', this); }

  /** @param {MouseEvent} e */
  onClick(e) {
    const t = /** @type {Element} */ (e.target);
    const toggle = t.closest('.add-edge');
    if (toggle) { this.request([edgeOf(toggle)], !toggle.classList.contains('on')); return; }
    const peer = t.closest('.peer');
    if (peer?.closest('[data-edge]')) { this.request([edgeOf(peer)], true); return; }
    const bulk = t.closest('.add-shown, .remove-shown');
    if (bulk) this.request(this.rows.map(edgeOf), bulk.classList.contains('add-shown'));
  }

  /** @param {any[]} edges @param {boolean} add */
  request(edges, add) {
    this.dispatchEvent(new CustomEvent('fgax:edges', { bubbles: true, detail: { edges: edges.filter(Boolean), add } }));
  }

  /**
   * Shows which edges are on the graph: checked, or "−" to remove.
   * @param {(edge: any) => boolean} isOn
   */
  mark(isOn) {
    const rows = this.rows;
    let n = 0;
    for (const li of rows) {
      const on = isOn(edgeOf(li));
      if (on) n++;
      const cb = /** @type {HTMLInputElement | null} */ ($('.edge-check', li));
      if (cb) cb.checked = on;
      const b = $('.add-edge', li);
      if (b) {
        b.textContent = on ? '−' : '＋';
        b.classList.toggle('on', on);
        b.title = on ? 'Remove this edge from the graph' : 'Add this edge to the graph';
        b.setAttribute('aria-label', b.title);
      }
    }
    const count = $('.sel-count', this);
    if (count) count.textContent = rows.length ? `${n} of ${rows.length} shown are on the graph` : '';
    const add = /** @type {HTMLButtonElement | null} */ ($('.add-shown', this));
    if (add) add.disabled = n === rows.length;
    const remove = /** @type {HTMLButtonElement | null} */ ($('.remove-shown', this));
    if (remove) remove.disabled = n === 0;
  }
}

export function initEdgeLists() {
  if (!customElements.get('fga-edge-list')) customElements.define('fga-edge-list', FgaEdgeList);
}
