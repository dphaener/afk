// sw-register.js registers the page's service worker (/sw.js) so the browser
// can offer to install the page as an app. It is a file of its own, not an
// inline script, because the page's content policy allows scripts from this
// site only. A browser without workers, or one that refuses the
// registration (a private window, plain http on a remote address), simply
// keeps the page as a page: nothing here is needed for it to work.
'use strict';

(function () {
  if (!('serviceWorker' in navigator)) {
    return;
  }
  navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(function () {});
})();
