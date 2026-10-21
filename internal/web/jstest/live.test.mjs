// Table tests of live.js's newRowIDs, which picks the rows of a swapped
// data-live-rows region that get the is-changed flash. Load-bearing rows:
// "one added" (a device signed up elsewhere flashes alone, the rows already
// there stay still), "from empty" (the first device arrives flashed after
// the empty state), "one removed" and "same rows reordered" (nothing new,
// so nothing flashes), and "empty id" (an id-less element is never new).
import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { newRowIDs } = require('../static/live.js');

const rows = [
  { name: 'one added', old: ['a', 'b'], now: ['c', 'a', 'b'], want: ['c'] },
  { name: 'from empty', old: [], now: ['a'], want: ['a'] },
  { name: 'one removed', old: ['a', 'b'], now: ['a'], want: [] },
  { name: 'same rows reordered', old: ['a', 'b'], now: ['b', 'a'], want: [] },
  { name: 'added and removed together', old: ['a', 'b'], now: ['b', 'c', 'd'], want: ['c', 'd'] },
  { name: 'to empty', old: ['a'], now: [], want: [] },
  { name: 'empty id', old: ['a'], now: ['a', ''], want: [] },
  { name: 'repeated id counted once', old: [], now: ['a', 'a'], want: ['a'] },
  { name: 'id named like an object key', old: [], now: ['constructor', '__proto__'], want: ['constructor', '__proto__'] },
  { name: 'missing lists', old: undefined, now: undefined, want: [] },
];

test('newRowIDs', async (t) => {
  for (const r of rows) {
    await t.test(r.name, () => {
      assert.deepEqual(newRowIDs(r.old, r.now), r.want);
    });
  }
});
