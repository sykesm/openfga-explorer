// Unit tests for the pure browser modules: node --test internal/web/jstest/
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { matchType, pushRecent, rankTypes, relTag, splitRels, typeWords } from '../static/js/typematch.js';

test('typeWords splits at spaces and identifier punctuation', () => {
  assert.deepEqual(typeWords('Jira proj'), ['jira', 'proj']);
  assert.deepEqual(typeWords('group#member'), ['group', 'member']);
  assert.deepEqual(typeWords('ns/billing.invoice-v2:x'), ['ns', 'billing', 'invoice', 'v2', 'x']);
  assert.deepEqual(typeWords('  '), []);
});

test('splitRels tolerates missing and repeated spaces', () => {
  assert.deepEqual(splitRels(undefined), []);
  assert.deepEqual(splitRels('owner  viewer '), ['owner', 'viewer']);
});

test('matchType scores name prefix, name contains, and relation matches', () => {
  const rels = ['owner', 'viewer', 'can_approve'];
  assert.deepEqual(matchType('jira_project', rels, []), { score: 0, rels: [] });
  assert.deepEqual(matchType('jira_project', rels, ['jira', 'proj']), { score: 0, rels: [] });
  assert.deepEqual(matchType('jira_project', rels, ['proj', 'jira']), { score: 1, rels: [] });
  assert.deepEqual(matchType('jira_project', rels, ['approve']), { score: 2, rels: ['can_approve'] });
  // Words missing from the name must all be in one relation.
  assert.deepEqual(matchType('jira_project', rels, ['jira', 'approve']), { score: 2, rels: ['can_approve'] });
  assert.equal(matchType('jira_project', rels, ['approve', 'viewer']), null);
  assert.equal(matchType('jira_project', rels, ['wiki']), null);
  // Case-insensitive on both sides.
  assert.deepEqual(matchType('Billing_Invoice', ['Can_Pay'], ['pay']), { score: 2, rels: ['Can_Pay'] });
});

test('relTag shows up to two relations', () => {
  assert.equal(relTag([]), '');
  assert.equal(relTag(['a']), 'a');
  assert.equal(relTag(['a', 'b']), 'a, b');
  assert.equal(relTag(['a', 'b', 'c', 'd']), 'a, b +2');
});

test('rankTypes orders by score, then model order', () => {
  const opts = [
    { label: 'xjira_board', rels: [] },
    { label: 'wiki_page', rels: ['jira_link'] },
    { label: 'jira_project', rels: [] },
    { label: 'jira_issue', rels: [] },
  ];
  assert.deepEqual(rankTypes(opts, 'jira').map((r) => r.o.label), ['jira_project', 'jira_issue', 'xjira_board', 'wiki_page']);
  assert.deepEqual(rankTypes(opts, '').map((r) => r.o.label), opts.map((o) => o.label));
  assert.deepEqual(rankTypes(opts, 'nothing'), []);
});

test('pushRecent moves a value to the front without duplicates, capped', () => {
  assert.deepEqual(pushRecent(['a', 'b', 'c'], 'b'), ['b', 'a', 'c']);
  assert.deepEqual(pushRecent(['a', 'b', 'c'], 'd', 3), ['d', 'a', 'b']);
  assert.deepEqual(pushRecent([], 'a'), ['a']);
});
