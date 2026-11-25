// Unit tests for core.js helpers that don't need a DOM.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { article, dagre, esc, labelHeight, labelWidth } from '../static/js/core.js';

test('esc escapes HTML metacharacters and tolerates nullish values', () => {
  assert.equal(esc('<a href="x">\'&\'</a>'), '&lt;a href=&quot;x&quot;&gt;&#39;&amp;&#39;&lt;/a&gt;');
  assert.equal(esc(null), '');
  assert.equal(esc(undefined), '');
  assert.equal(esc(42), '42');
});

test('article picks a/an by the first letter', () => {
  assert.equal(article('organization'), 'an');
  assert.equal(article('group'), 'a');
  assert.equal(article('User'), 'a');
  assert.equal(article('unit'), 'a');
  assert.equal(article('umbrella'), 'an');
  assert.equal(article('update'), 'an');
  assert.equal(article('invoice'), 'an');
});

test('label sizes grow with the longest line and the line count', () => {
  assert.equal(labelWidth(''), 40);
  assert.ok(labelWidth('document:roadmap\n#viewer') > labelWidth('#viewer'));
  assert.equal(labelHeight('a\nb\nc'), 3 * 15 + 14);
});

test('dagre options merge overrides', () => {
  assert.deepEqual(dagre('LR', { rankSep: 1 }), { name: 'dagre', rankDir: 'LR', nodeSep: 24, rankSep: 1, edgeSep: 10, padding: 24, animate: false });
});
