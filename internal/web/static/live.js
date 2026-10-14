// live.js keeps an open afk page current. It listens to the server's change
// stream (GET /events), fetches the page again when something it shows may
// have changed, and swaps the marked regions in place. Without it, or when
// the stream is refused, the page works exactly as before: reload it.
'use strict';

(function () {
  if (!('EventSource' in window) || !('fetch' in window) || !('DOMParser' in window)) {
    return;
  }

  var COALESCE_MS = 300;
  var CHANGED_MS = 1600;
  var BACKOFF_START_MS = 3000;
  var BACKOFF_MAX_MS = 30000;

  var status = document.querySelector('[data-live-status]');
  var source = null;
  var backoff = BACKOFF_START_MS;
  var reconnectTimer = null;
  var fetchTimer = null;
  // pending holds the regions to refresh, by element; everything means all.
  var pending = [];
  var everything = false;

  function setStatus(online) {
    if (!status) {
      return;
    }
    status.hidden = false;
    status.className = online ? 'live' : 'live live--offline';
    status.textContent = online ? 'Live' : "Can't reach afk, retrying";
  }

  function regions() {
    return Array.prototype.slice.call(document.querySelectorAll('[data-live]'));
  }

  function parts(el) {
    return (el.getAttribute('data-live') || '').split(/\s+/).filter(Boolean);
  }

  // matches reports whether a change event concerns a region: the part is one
  // it lists, and the project is its own. Machine-wide parts (project "")
  // reach only regions with no project.
  function matches(el, ev) {
    return parts(el).indexOf(ev.part) !== -1 &&
      (el.getAttribute('data-project') || '') === (ev.project || '');
  }

  function schedule() {
    if (fetchTimer !== null) {
      return;
    }
    fetchTimer = setTimeout(refresh, COALESCE_MS);
  }

  function refresh() {
    fetchTimer = null;
    var wanted = everything ? regions() : pending.slice();
    pending = [];
    everything = false;
    if (wanted.length === 0) {
      return;
    }
    fetch(location.href, { credentials: 'same-origin', cache: 'no-store' })
      .then(function (res) {
        if (!res.ok) {
          throw new Error('status ' + res.status);
        }
        return res.text();
      })
      .then(function (text) {
        var doc = new DOMParser().parseFromString(text, 'text/html');
        wanted.forEach(function (old) {
          swap(old, doc);
        });
      })
      .catch(function () {
        // A failed fetch changes nothing on screen and never navigates; the
        // next event or reconnect tries again.
      });
  }

  function swap(old, doc) {
    if (!old.id || !document.contains(old)) {
      return;
    }
    // Never take the page out from under a finger: a region holding focus
    // (or a form) is left as it is.
    if (old.contains(document.activeElement) || old.querySelector('form, input, textarea, select')) {
      return;
    }
    var fresh = doc.getElementById(old.id);
    if (!fresh) {
      return;
    }
    var node = document.importNode(fresh, true);
    old.replaceWith(node);
    node.classList.add('is-changed');
    setTimeout(function () {
      node.classList.remove('is-changed');
    }, CHANGED_MS);
  }

  function onChange(e) {
    var ev;
    try {
      ev = JSON.parse(e.data);
    } catch (err) {
      return;
    }
    regions().forEach(function (el) {
      if (matches(el, ev) && pending.indexOf(el) === -1) {
        pending.push(el);
      }
    });
    if (pending.length > 0) {
      schedule();
    }
  }

  function connect() {
    reconnectTimer = null;
    source = new EventSource('/events');
    source.addEventListener('open', function () {
      setStatus(true);
      backoff = BACKOFF_START_MS;
      // The first open also refreshes: the stream's reader takes its first
      // look at the files only after a stream opens, so a change between
      // the page's own load and that look would otherwise never be sent.
      // After a reconnect the events in between were missed the same way.
      everything = true;
      schedule();
    });
    source.addEventListener('change', onChange);
    source.addEventListener('error', function () {
      setStatus(false);
      // The browser retries by itself while the stream is merely CONNECTING.
      // CLOSED means it gave up (a refusal such as 401 or 503), so retry
      // here, slower each time.
      if (source.readyState === EventSource.CLOSED) {
        source.close();
        if (reconnectTimer === null) {
          reconnectTimer = setTimeout(connect, backoff);
          backoff = Math.min(backoff * 2, BACKOFF_MAX_MS);
        }
      }
    });
  }

  connect();
})();
