// sw.js is the page's service worker, served at /sw.js so its scope is the
// whole site. A browser offers to install a page as an app only when it has
// a manifest and a worker. This one takes over at once and shows the
// notifications the server pushes (the push and notificationclick handlers
// below).
//
// It deliberately has no fetch handler and keeps no cache. Every page shows
// live state (a task's phase, the inbox, usage), so a copy served from a
// cache would show something no longer true; with no fetch handler every
// request goes to the network exactly as it would without a worker. Keep it
// that way: push needs no fetch handler.
'use strict';

// The server fills this in when it serves /sw.js (see serviceWorker in
// manifest.go) with the hashed address of icons/icon-192.png, the icon a
// notification shows, or '' when the binary has no such icon. The icon's
// address changes with its content, so it cannot be written here.
var AFK_ICON = '__AFK_ICON__';


// safePath turns the url a push carries into an address on this site. Only a
// path that starts with one slash is accepted: '//evil' and '/\\evil' are
// read by a browser as another site, and 'https://evil' is another site. A
// push is signed by afk, but the worker does not trust the payload to be
// well-formed.
function safePath(url) {
  if (typeof url !== 'string' || url.charAt(0) !== '/') {
    return '/';
  }
  var second = url.charAt(1);
  if (second === '/' || second === '\\') {
    return '/';
  }
  return url;
}

if (typeof module !== 'undefined') {
  module.exports = { safePath: safePath };
}

// self is guarded so a node test can load this file for safePath.
if (typeof self !== 'undefined' && self.addEventListener) {
  self.addEventListener('install', function () {
    self.skipWaiting();
  });

  self.addEventListener('activate', function (event) {
    event.waitUntil(self.clients.claim());
  });

  self.addEventListener('push', function (event) {
    var data = {};
    try {
      data = (event.data && event.data.json()) || {};
    } catch (e) {
      data = {};
    }
    if (typeof data !== 'object' || data === null) {
      data = {};
    }
    var title = typeof data.title === 'string' && data.title ? data.title : 'afk';
    var options = {
      body: typeof data.body === 'string' ? data.body : '',
      tag: typeof data.tag === 'string' && data.tag ? data.tag : undefined,
      data: { url: typeof data.url === 'string' ? data.url : '' }
    };
    if (AFK_ICON) {
      options.icon = AFK_ICON;
    }
    event.waitUntil(self.registration.showNotification(title, options));
  });

  self.addEventListener('notificationclick', function (event) {
    event.notification.close();
    var raw = event.notification.data && event.notification.data.url;
    var target = new URL(safePath(raw), self.location.origin).href;
    event.waitUntil(
      self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(function (list) {
        if (list.length > 0) {
          var win = list[0];
          return win.focus().then(function (focused) {
            var w = focused || win;
            return 'navigate' in w ? w.navigate(target) : null;
          });
        }
        return self.clients.openWindow(target);
      })
    );
  });
}
