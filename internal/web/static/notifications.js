// notifications.js runs the "This device" card on the Notifications page: it
// reads what this browser can do, shows the one panel that fits, and turns
// notifications on or off and sends a test. Without it (JavaScript off) the
// card shows only its "needs JavaScript" notice.
//
// Markup hooks (the template builds them):
//   [data-push-card]       the card; data-public-key holds the key to subscribe with
//   [data-push-state=X]    one panel per state (https, homescreen, unsupported,
//                          blocked, on, off), each `hidden` until chosen
//   [data-push-badge]      the badge; gets badge--on, badge--blocked or badge--off
//   [data-push-on]  [data-push-off]  [data-push-test]  [data-push-recheck]
//                          the buttons
//   [data-push-nojs]       the no-JavaScript notice; this script hides it
//
// The state choice is a pure function so a node test can load this file.
'use strict';

// deviceState picks the one state to show. The order is the point: an
// insecure page cannot have push at all, so it wins over everything; an
// iPhone not yet on the home screen cannot ask, whatever else is true; a
// browser without push cannot be blocked; and a denied permission beats a
// leftover subscription, which can no longer deliver anything.
function deviceState(env) {
  if (!env.secure) {
    return 'https';
  }
  if (env.ios && !env.standalone) {
    return 'homescreen';
  }
  if (!env.hasPush) {
    return 'unsupported';
  }
  if (env.permission === 'denied') {
    return 'blocked';
  }
  return env.subscribed ? 'on' : 'off';
}

// staleAfterTest is true when a Send test answer means the server does not
// list this device: 410 (the push service said the subscription is over, so
// the server removed it) or 404 (it was never listed, or was removed
// elsewhere). The browser still holds its own subscription then, so without
// dropping it the card would keep showing On with no way to turn back on.
// Any other failure (a 502 from a slow push service) leaves the device
// listed, so the subscription stays.
function staleAfterTest(status) {
  return status === 404 || status === 410;
}

// isAppleMobile is true for iPhone and iPad. iPadOS 13 and later report a
// Mac platform, so a Mac with more than one touch point is an iPad.
function isAppleMobile(userAgent, platform, maxTouchPoints) {
  if (/iPhone|iPad|iPod/.test(userAgent || '')) {
    return true;
  }
  return platform === 'MacIntel' && (maxTouchPoints || 0) > 1;
}

// urlBase64ToUint8Array turns the URL-safe base64 VAPID key into the bytes
// pushManager.subscribe wants as applicationServerKey.
function urlBase64ToUint8Array(s) {
  var padding = '='.repeat((4 - (s.length % 4)) % 4);
  var b64 = (s + padding).replace(/-/g, '+').replace(/_/g, '/');
  var raw = atob(b64);
  var out = new Uint8Array(raw.length);
  for (var i = 0; i < raw.length; i++) {
    out[i] = raw.charCodeAt(i);
  }
  return out;
}

if (typeof module !== 'undefined') {
  module.exports = {
    deviceState: deviceState,
    staleAfterTest: staleAfterTest,
    isAppleMobile: isAppleMobile,
    urlBase64ToUint8Array: urlBase64ToUint8Array
  };
}

(function () {
  if (typeof document === 'undefined' || typeof window === 'undefined') {
    return;
  }
  var card = document.querySelector('[data-push-card]');
  if (!card) {
    return;
  }

  var BADGES = {
    on: ['badge--on', 'On'],
    blocked: ['badge--blocked', 'Blocked'],
    off: ['badge--off', 'Off']
  };
  var BADGE_CLASSES = ['badge--on', 'badge--blocked', 'badge--off'];

  var nojs = card.querySelector('[data-push-nojs]') || document.querySelector('[data-push-nojs]');
  if (nojs) {
    nojs.hidden = true;
  }

  function one(sel) {
    return card.querySelector(sel);
  }

  function hasPush() {
    return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
  }

  // currentSubscription is this browser's push subscription, or null. It
  // never waits on a worker that was never registered.
  function currentSubscription() {
    if (!hasPush()) {
      return Promise.resolve(null);
    }
    return navigator.serviceWorker.getRegistration().then(function (reg) {
      return reg ? reg.pushManager.getSubscription() : null;
    }).catch(function () {
      return null;
    });
  }

  function readEnv() {
    return currentSubscription().then(function (sub) {
      var standalone = !!navigator.standalone ||
        (window.matchMedia && window.matchMedia('(display-mode: standalone)').matches);
      return {
        secure: !!window.isSecureContext,
        ios: isAppleMobile(navigator.userAgent, navigator.platform, navigator.maxTouchPoints),
        standalone: standalone,
        hasPush: hasPush(),
        permission: hasPush() ? Notification.permission : 'default',
        subscribed: !!sub
      };
    });
  }

  function draw() {
    return readEnv().then(function (env) {
      var state = deviceState(env);
      Array.prototype.slice.call(card.querySelectorAll('[data-push-state]')).forEach(function (p) {
        p.hidden = p.getAttribute('data-push-state') !== state;
      });
      var badge = one('[data-push-badge]');
      if (badge) {
        BADGE_CLASSES.forEach(function (c) {
          badge.classList.remove(c);
        });
        var b = BADGES[state] || BADGES.off;
        badge.classList.add(b[0]);
        badge.textContent = b[1];
      }
      return state;
    });
  }

  // say shows words through the layout's toast region, emptied first and
  // filled after a beat so a screen reader announces the change (the way
  // submit.js does it).
  function say(words) {
    var toast = document.querySelector('[data-toast]');
    if (!toast) {
      var region = document.createElement('div');
      region.className = 'toast-region';
      toast = document.createElement('div');
      toast.className = 'toast';
      toast.setAttribute('role', 'status');
      toast.setAttribute('data-toast', '');
      region.appendChild(toast);
      document.body.appendChild(region);
    }
    toast.textContent = '';
    setTimeout(function () {
      toast.textContent = words;
    }, 100);
  }

  function post(path, body) {
    return fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      credentials: 'same-origin'
    }).then(function (res) {
      return res.json().then(function (data) {
        return { status: res.status, ok: res.ok, data: data || {} };
      }, function () {
        return { status: res.status, ok: res.ok, data: {} };
      });
    });
  }

  // busy disables a button and swaps its words while work runs, and returns
  // the function that puts both back.
  function busy(btn, words) {
    var old = btn.textContent;
    btn.disabled = true;
    btn.textContent = words;
    return function () {
      btn.disabled = false;
      btn.textContent = old;
    };
  }

  function failure(err) {
    return (err && err.message) || 'Something went wrong. Try again.';
  }

  function turnOn(btn) {
    var done = busy(btn, 'Turning on');
    var key = card.getAttribute('data-public-key') || '';
    var sub = null;
    Notification.requestPermission().then(function (perm) {
      if (perm !== 'granted') {
        return null;
      }
      return navigator.serviceWorker.ready.then(function (reg) {
        return reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(key)
        });
      }).then(function (s) {
        sub = s;
        return post('/api/push/subscribe', s.toJSON());
      }).then(function (r) {
        if (!r.ok) {
          return sub.unsubscribe().then(function () {
            throw new Error(r.data.error || 'afk could not sign this device up. Try again.');
          });
        }
      });
    }).catch(function (err) {
      say(failure(err));
    }).then(function () {
      done();
      return draw();
    });
  }

  function turnOff(btn) {
    var done = busy(btn, 'Turning off');
    currentSubscription().then(function (sub) {
      if (!sub) {
        return null;
      }
      return post('/api/push/unsubscribe', { endpoint: sub.endpoint }).then(function () {
        return sub.unsubscribe();
      });
    }).catch(function (err) {
      say(failure(err));
    }).then(function () {
      done();
      return draw();
    });
  }

  function sendTest(btn) {
    var done = busy(btn, 'Sending test');
    var reread = false;
    currentSubscription().then(function (sub) {
      if (!sub) {
        reread = true;
        say('This device is not signed up for notifications. Turn them on first.');
        return null;
      }
      return post('/api/push/test', { endpoint: sub.endpoint }).then(function (r) {
        if (r.ok) {
          say('Test sent. It should arrive in a few seconds.');
          return null;
        }
        say(r.data.error || 'The test could not be sent. Try again in a minute.');
        if (!staleAfterTest(r.status)) {
          return null;
        }
        // The server no longer lists this device, so drop the browser's own
        // subscription too; the redraw then shows the Off panel and its
        // Turn on button, which signs the device up afresh. A failed
        // unsubscribe is ignored: the server's words are already showing.
        reread = true;
        return sub.unsubscribe().catch(function () {
          return null;
        });
      });
    }).catch(function (err) {
      say(failure(err));
    }).then(function () {
      done();
      return reread ? draw() : null;
    });
  }

  card.addEventListener('click', function (ev) {
    var t = ev.target;
    var btn = t && t.closest ? t.closest('button') : null;
    if (!btn || btn.disabled || !card.contains(btn)) {
      return;
    }
    if (btn.hasAttribute('data-push-on')) {
      turnOn(btn);
    } else if (btn.hasAttribute('data-push-off')) {
      turnOff(btn);
    } else if (btn.hasAttribute('data-push-test')) {
      sendTest(btn);
    } else if (btn.hasAttribute('data-push-recheck')) {
      draw();
    }
  });

  draw();
})();
