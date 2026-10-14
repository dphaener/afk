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

  // The indicator is in each bar (the phone bar and the desktop bar, one
  // shown per width). The phone bar's carries data-live-status, the desktop
  // bar's data-live-mirror, and both are given the same class and words.
  function findIndicators() {
    return Array.prototype.slice.call(document.querySelectorAll('[data-live-status], [data-live-mirror]'));
  }
  var indicators = findIndicators();
  var source = null;
  var backoff = BACKOFF_START_MS;
  var reconnectTimer = null;
  var fetchTimer = null;
  // online is null until the stream first opens or fails, so the indicator
  // stays hidden (as it is without JavaScript) until there is something true
  // to say.
  var online = null;
  // pending holds the regions to refresh, by element; everything means all.
  var pending = [];
  var everything = false;

  function setStatus(isOnline) {
    online = isOnline;
    render();
  }

  // working reports whether anything on the page says afk is busy on it. A
  // region marks that with data-working="true", so it changes with the
  // swapped-in regions and is looked at again after every swap.
  function working() {
    return document.querySelector('[data-working="true"]') !== null;
  }

  function render() {
    if (online === null) {
      return;
    }
    var cls = 'live live--offline';
    var text = "Can't reach afk, retrying";
    if (online && working()) {
      cls = 'live live--working';
      text = 'Working';
    } else if (online) {
      cls = 'live';
      text = 'Live';
    }
    indicators.forEach(function (el) {
      el.hidden = false;
      el.className = el.hasAttribute('data-live-mirror') ? cls + ' topnav__live' : cls;
      el.textContent = text;
    });
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
        render();
        // submit.js restarts the "Updated ... ago" count for a fresh region.
        document.dispatchEvent(new CustomEvent('afk:live'));
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
    // A region that opts in with data-live-buttons holds a form of buttons
    // only (the Refresh usage button). Its whole point is to change under
    // the press: the button turns to "Refreshing" and later to "Checked just
    // now", and the press leaves focus on it, so the plain rule below would
    // keep it from ever updating. It is swapped unless a typed field has
    // focus, and focus is handed to the new copy of the button with the same
    // id so a keyboard user does not lose their place.
    var buttons = old.hasAttribute('data-live-buttons');
    var active = document.activeElement;
    if (buttons) {
      if (old.contains(active) && active.matches('input, textarea, select')) {
        return;
      }
    } else if (old.contains(active) || old.querySelector('form, input, textarea, select')) {
      // Never take the page out from under a finger: a region holding focus
      // (or a form) is left as it is.
      return;
    }
    var fresh = doc.getElementById(old.id);
    if (!fresh) {
      return;
    }
    var heldId = buttons && old.contains(active) ? active.id : '';
    var node = document.importNode(fresh, true);
    old.replaceWith(node);
    if (heldId) {
      var again = document.getElementById(heldId);
      if (again) {
        again.focus({ preventScroll: true });
      }
    }
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

  // submit.js swaps the whole page after a form post (afk:swapped). The bars
  // came with it, so the indicators are found again and given the stream's
  // state; a refresh queued for the old page's regions is dropped, since the
  // new page was read after it.
  document.addEventListener('afk:swapped', function () {
    indicators = findIndicators();
    pending = [];
    everything = false;
    render();
  });

  connect();
})();
