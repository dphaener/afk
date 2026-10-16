// Table test of chat.js's stalePending. Load-bearing row: "choice reply" -- the
// server writes "Header: Label" for a choice, so the owner bubble's text never
// equals the pending words and only the count rule can prune it (the bug that
// pinned a "Sent" bubble below the thread).
import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { stalePending } = require('../static/chat.js');

const rows = [
  { name: 'typed reply whose bubble arrived', sent: 0, now: 1, text: 'hello', owners: ['hello'], want: true },
  { name: 'choice reply with different text', sent: 0, now: 1, text: 'Yes', owners: ['Proceed: Yes'], want: true },
  { name: 'no new owner message yet', sent: 1, now: 1, text: 'Yes', owners: ['earlier'], want: false },
  { name: 'two pending, older one arrived', sent: 0, now: 1, text: 'A', owners: ['Q: A'], want: true },
  { name: 'two pending, newer one still waits', sent: 1, now: 1, text: 'B', owners: ['Q: A'], want: false },
];

test('stalePending', async (t) => {
  for (const r of rows) {
    await t.test(r.name, () => {
      assert.equal(stalePending(r.sent, r.now, r.text, r.owners), r.want);
    });
  }
});
