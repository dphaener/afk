// Table tests of watch.js's pure helpers. Load-bearing rows: "other session"
// (a change for another session's screen must not fetch ours) and "missing
// header" (a response without X-Watch-Slow must poll at the fast 2 s, not stall).
// The frame rows pin that the CSP line is present and the fragment untouched.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { watchFrameSrcdoc, watchPollHint, watchSessionMatches } = require('../static/watch.js');

const CSP = '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'">';

test('watchFrameSrcdoc', async (t) => {
  const rows = [
    { name: 'plain fragment', in: '<pre>hi</pre>' },
    { name: 'style and script text', in: '<style>p{}</style><script>alert(1)</script>' },
    { name: 'empty', in: '' },
  ];
  for (const r of rows) {
    await t.test(r.name, () => {
      const out = watchFrameSrcdoc(r.in);
      assert.ok(out.includes(CSP));
      assert.ok(out.includes(r.in));
      assert.equal(out, CSP + r.in);
    });
  }
});

test('watchPollHint', async (t) => {
  const rows = [
    { name: 'slow', headers: new Headers({ 'X-Watch-Slow': '1' }), want: 10000 },
    { name: 'not slow', headers: new Headers({ 'X-Watch-Slow': '0' }), want: 2000 },
    { name: 'missing header', headers: new Headers(), want: 2000 },
    { name: 'no headers object', headers: undefined, want: 2000 },
  ];
  for (const r of rows) {
    await t.test(r.name, () => {
      assert.equal(watchPollHint(r.headers), r.want);
    });
  }
});

test('watchSessionMatches', async (t) => {
  const rows = [
    { name: 'right session', ch: { project: 'p', part: 'screen', session: 's' }, want: true },
    { name: 'other session', ch: { project: 'p', part: 'screen', session: 'x' }, want: false },
    { name: 'other project', ch: { project: 'q', part: 'screen', session: 's' }, want: false },
    { name: 'non-screen part', ch: { project: 'p', part: 'status', session: 's' }, want: false },
    { name: 'no change', ch: null, want: false },
  ];
  for (const r of rows) {
    await t.test(r.name, () => {
      assert.equal(watchSessionMatches(r.ch, 'p', 's'), r.want);
    });
  }
});
