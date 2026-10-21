// sw.js is the page's service worker, served at /sw.js so its scope is the
// whole site. A browser offers to install a page as an app only when it has
// a manifest and a worker, so this one exists to be there: it takes over at
// once and does nothing else. T15.5 adds its push handler here.
//
// It deliberately has no fetch handler and keeps no cache. Every page shows
// live state (a task's phase, the inbox, usage), so a copy served from a
// cache would show something no longer true; with no fetch handler every
// request goes to the network exactly as it would without a worker.
'use strict';

self.addEventListener('install', function () {
  self.skipWaiting();
});

self.addEventListener('activate', function (event) {
  event.waitUntil(self.clients.claim());
});
