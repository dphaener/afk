// watch.js keeps the Watch page's agent screen current. The page holds a
// script-free sandboxed frame (#agent-watch-frame) inside #agent-watch. While
// the page is open this script pings the server so it knows someone is
// watching, listens to the change stream (GET /events), and when the server
// says this session's screen changed it fetches the live frame fragment and
// puts it in the frame. It is read-only: the only requests it makes are the
// ping and the screen fragment, and it never sends a key to the agent.
// Without EventSource the page keeps the frame it was loaded with and says so.
'use strict';

// WATCH_CSP is fixed. The screen text comes from an agent, so the frame
// forbids every load and every script; only inline styles (the palette) run.
var WATCH_CSP =
  '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'">';

// watchFrameSrcdoc is the frame's document: the CSP line, then the fragment
// unchanged. The server (watchFragment in watch_handlers.go) puts the palette as a <style> at the head of the fragment,
// so this takes none. The meta comes first so it governs everything after it.
function watchFrameSrcdoc(fragment) {
  return WATCH_CSP + String(fragment == null ? '' : fragment);
}

// watchPollHint is the milliseconds before the next poll, from the
// X-Watch-Slow response header: "1" means the screen is above the size bound
// and refreshes slowly. A missing or other value means the fast cadence.
function watchPollHint(headers) {
  var v = null;
  if (headers && typeof headers.get === 'function') {
    v = headers.get('X-Watch-Slow');
  } else if (headers && typeof headers === 'object') {
    v = headers['X-Watch-Slow'];
  }
  return v === '1' ? 10000 : 2000;
}

// watchSessionMatches reports whether a change event names this watched
// session's screen. Another session's screen, another project's, or another
// part of the page is not ours: matching it would fetch for nothing.
function watchSessionMatches(change, project, session) {
  return !!change && change.part === 'screen' && change.project === project && change.session === session;
}

if (typeof module !== 'undefined') {
  module.exports = {
    watchFrameSrcdoc: watchFrameSrcdoc,
    watchPollHint: watchPollHint,
    watchSessionMatches: watchSessionMatches
  };
}

(function () {
  if (typeof window === 'undefined' || typeof document === 'undefined') {
    return;
  }
  var root = document.getElementById('agent-watch');
  if (!root) {
    return;
  }
  var project = root.getAttribute('data-project');
  var session = root.getAttribute('data-session');
  var frame = document.getElementById('agent-watch-frame');
  var status = document.getElementById('agent-watch-status');
  if (!project || !session || !frame) {
    return;
  }

  var PING_MS = 4000;
  var base = '/p/' + encodeURIComponent(project) + '/sessions/' + encodeURIComponent(session) + '/watch';

  function say(text) {
    if (status) {
      status.textContent = text;
    }
  }

  if (!('EventSource' in window) || !('fetch' in window)) {
    say('This browser cannot follow the screen live. Reload the page to see it again.');
    return;
  }

  var fetching = false;
  var again = false;
  var minGap = 2000;
  var lastFetch = 0;
  var gapTimer = null;

  function refresh() {
    if (fetching) {
      again = true;
      return;
    }
    var wait = lastFetch + minGap - Date.now();
    if (wait > 0) {
      if (gapTimer === null) {
        gapTimer = setTimeout(function () {
          gapTimer = null;
          refresh();
        }, wait);
      }
      return;
    }
    fetching = true;
    lastFetch = Date.now();
    fetch(base + '/screen', { credentials: 'same-origin', cache: 'no-store' })
      .then(function (res) {
        if (!res.ok) {
          throw new Error('status ' + res.status);
        }
        minGap = watchPollHint(res.headers);
        return res.text();
      })
      .then(function (text) {
        frame.srcdoc = watchFrameSrcdoc(text);
        say('Last updated ' + new Date().toLocaleTimeString());
      })
      .catch(function () {
        say("Can't reach afk, retrying");
      })
      .then(function () {
        fetching = false;
        if (again) {
          again = false;
          refresh();
        }
      });
  }

  function ping() {
    fetch(base + '/ping', { method: 'POST', credentials: 'same-origin' }).catch(function () {});
  }

  ping();
  setInterval(ping, PING_MS);

  var source = new EventSource('/events');
  source.addEventListener('change', function (e) {
    var ev;
    try {
      ev = JSON.parse(e.data);
    } catch (err) {
      return;
    }
    if (watchSessionMatches(ev, project, session)) {
      refresh();
    }
  });
})();
