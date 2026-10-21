// Table tests of notifications.js (deviceState, staleAfterTest,
// isAppleMobile, urlBase64ToUint8Array) and sw.js's safePath. Load-bearing
// rows: the "beats" rows of deviceState, which pin the order (http first,
// then the iPhone home-screen need, then unsupported, then blocked, then
// subscribed); the 404 and 410 rows of staleAfterTest, which drop a
// subscription the server no longer lists so the card can offer Turn on
// again, and the 502 row, which keeps a listed device signed up through a
// passing push-service failure; and the "//evil" and "/\\evil" rows of
// safePath, which stop a push from opening another site.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { deviceState, staleAfterTest, isAppleMobile, urlBase64ToUint8Array } = require('../static/notifications.js');
const { safePath } = require('../static/sw.js');

const ok = { secure: true, ios: false, standalone: false, hasPush: true, permission: 'default', subscribed: false };

const stateRows = [
  { name: 'http', env: { ...ok, secure: false }, want: 'https' },
  { name: 'iphone not installed', env: { ...ok, ios: true }, want: 'homescreen' },
  { name: 'no push support', env: { ...ok, hasPush: false }, want: 'unsupported' },
  { name: 'permission denied', env: { ...ok, permission: 'denied' }, want: 'blocked' },
  { name: 'subscribed', env: { ...ok, permission: 'granted', subscribed: true }, want: 'on' },
  { name: 'not subscribed', env: ok, want: 'off' },
  { name: 'iphone installed is off', env: { ...ok, ios: true, standalone: true }, want: 'off' },
  { name: 'http beats everything', env: { secure: false, ios: true, standalone: false, hasPush: false, permission: 'denied', subscribed: true }, want: 'https' },
  { name: 'iphone not installed beats unsupported and blocked', env: { ...ok, ios: true, hasPush: false, permission: 'denied' }, want: 'homescreen' },
  { name: 'unsupported beats blocked', env: { ...ok, hasPush: false, permission: 'denied' }, want: 'unsupported' },
  { name: 'blocked beats subscribed', env: { ...ok, permission: 'denied', subscribed: true }, want: 'blocked' },
];

test('deviceState', async (t) => {
  for (const r of stateRows) {
    await t.test(r.name, () => {
      assert.equal(deviceState(r.env), r.want);
    });
  }
});

const staleRows = [
  { name: 'sent', status: 200, want: false },
  { name: 'not listed', status: 404, want: true },
  { name: 'gone', status: 410, want: true },
  { name: 'push service failed', status: 502, want: false },
];

test('staleAfterTest', async (t) => {
  for (const r of staleRows) {
    await t.test(r.name, () => {
      assert.equal(staleAfterTest(r.status), r.want);
    });
  }
});

const uaIphone = 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15';
const uaIpad = 'Mozilla/5.0 (iPad; CPU OS 16_0 like Mac OS X) AppleWebKit/605.1.15';
const uaMac = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Safari/605.1.15';
const uaAndroid = 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/120 Mobile Safari/537.36';

const appleRows = [
  { name: 'iPhone', ua: uaIphone, platform: 'iPhone', touch: 5, want: true },
  { name: 'iPad', ua: uaIpad, platform: 'iPad', touch: 5, want: true },
  { name: 'iPadOS reporting MacIntel with touch', ua: uaMac, platform: 'MacIntel', touch: 5, want: true },
  { name: 'real Mac', ua: uaMac, platform: 'MacIntel', touch: 0, want: false },
  { name: 'Android', ua: uaAndroid, platform: 'Linux armv81', touch: 5, want: false },
];

test('isAppleMobile', async (t) => {
  for (const r of appleRows) {
    await t.test(r.name, () => {
      assert.equal(isAppleMobile(r.ua, r.platform, r.touch), r.want);
    });
  }
});

test('urlBase64ToUint8Array round trip', () => {
  const bytes = Uint8Array.from([251, 255, 0, 1, 62, 63, 250]);
  const s = Buffer.from(bytes).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  assert.deepEqual(Array.from(urlBase64ToUint8Array(s)), Array.from(bytes));
});

const pathRows = [
  { name: 'own path', in: '/p/x', want: '/p/x' },
  { name: 'root', in: '/', want: '/' },
  { name: 'protocol-relative', in: '//evil', want: '/' },
  { name: 'absolute url', in: 'https://evil', want: '/' },
  { name: 'backslash', in: '/\\evil', want: '/' },
  { name: 'empty', in: '', want: '/' },
  { name: 'not a string', in: undefined, want: '/' },
];

test('safePath', async (t) => {
  for (const r of pathRows) {
    await t.test(r.name, () => {
      assert.equal(safePath(r.in), r.want);
    });
  }
});
