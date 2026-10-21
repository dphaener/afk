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
//   #push-device-list      the signed-up devices list (a live.js region); the
//                          script replaces it after removing this device and
//                          after the load-time lookup marks this device
//   a[data-push-remove]    a row's Remove link: data-device-id, data-device-label,
//                          and data-this-device on this device's row. Without
//                          the script it goes to the no-JavaScript confirm page
//   #push-remove-sheet     the one shared confirm dialog; its -title, -body and
//                          -form (hidden input name="id") are filled per row
//
// It also removes devices: a row's Remove opens the shared dialog, filled
// with the row's words, and Remove there posts the dialog's form (another
// device) or runs Turn off (this device). At load, a browser holding a
// subscription asks the server whether it is still listed, and turns itself
// off when it is not: that is how a device removed elsewhere shows Off.
//
// The state choice, the dialog's words and the lookup's outcome are pure
// functions so a node test can load this file.
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

// REMOVE_OTHER and REMOVE_THIS are the dialog's sentences. They are the
// server's words (deviceRemoveOther and deviceRemoveThis in push.go, which
// the no-JavaScript confirm page shows), so the dialog and the page say the
// same thing; change them together.
var REMOVE_OTHER = 'It stops getting notifications. To get them again, open afk on it and turn them on.';
var REMOVE_THIS = 'This device stops getting notifications.';

// removeSheetText is the shared dialog's question and sentence for one row.
// Another device is told how to come back, since removing it does not stop
// the browser there from asking again; this device is turned off in front
// of the owner, so it needs no such line.
function removeSheetText(label, thisDevice) {
  return {
    title: 'Remove ' + label + '?',
    body: thisDevice ? REMOVE_THIS : REMOVE_OTHER
  };
}

// afterLookup decides what the page does with the server's answer to "is
// this browser's subscription still listed?" (POST /api/push/device), given
// the id of the row the page marks as this device ('' for none):
//   "off"     the server does not list it (removed from another device, or
//             dropped by the push service): drop the browser's subscription
//             and show Off, or the card would show On with nothing behind it
//   "refresh" listed, but the page marks another row or none: the lookup has
//             just set the device cookie, so a fresh copy of the list marks
//             this device and puts it first
//   "keep"    listed and already marked, or no usable answer (a failed
//             fetch, a refusal): the page stays as the server drew it, since
//             guessing Off on a network blip would drop a working device
function afterLookup(answer, markedID) {
  if (!answer || typeof answer !== 'object') {
    return 'keep';
  }
  if (answer.listed === false) {
    return 'off';
  }
  if (answer.listed === true && typeof answer.id === 'string' && answer.id !== '' && answer.id !== markedID) {
    return 'refresh';
  }
  return 'keep';
}

if (typeof module !== 'undefined') {
  module.exports = {
    deviceState: deviceState,
    staleAfterTest: staleAfterTest,
    isAppleMobile: isAppleMobile,
    urlBase64ToUint8Array: urlBase64ToUint8Array,
    removeSheetText: removeSheetText,
    afterLookup: afterLookup
  };
}

(function () {
  if (typeof document === 'undefined' || typeof window === 'undefined') {
    return;
  }

  var BADGES = {
    on: ['badge--on', 'On'],
    blocked: ['badge--blocked', 'Blocked'],
    off: ['badge--off', 'Off']
  };
  var BADGE_CLASSES = ['badge--on', 'badge--blocked', 'badge--off'];
  var SHEET_ID = 'push-remove-sheet';
  var LIST_ID = 'push-device-list';

  // card is found again after submit.js swaps the page (afk:swapped):
  // removing another device posts the dialog's form, and the page it lands
  // on brings a new card that nothing has drawn yet. Without a key the
  // page has no card, and only the Remove links are handled.
  var card = null;

  function findCard() {
    card = document.querySelector('[data-push-card]');
    if (!card) {
      return false;
    }
    var nojs = card.querySelector('[data-push-nojs]') || document.querySelector('[data-push-nojs]');
    if (nojs) {
      nojs.hidden = true;
    }
    return true;
  }

  function one(sel) {
    return card ? card.querySelector(sel) : null;
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
    if (!card) {
      return Promise.resolve('');
    }
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
    var holder = toast.parentNode;
    if (holder && holder.classList.contains('toast-region')) {
      holder.hidden = false;
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

  // markedID is the id of the row the server marked as this device, or ''.
  function markedID() {
    var link = document.querySelector('#' + LIST_ID + ' a[data-push-remove][data-this-device]');
    return link ? link.getAttribute('data-device-id') || '' : '';
  }

  // refreshList fetches this page again and puts its devices list in place
  // of the one showing. live.js keeps the list current from the change
  // stream, but the two moments here change nothing in the device file
  // (the load lookup only sets the device cookie) or must not wait for the
  // stream (this device just removed), so the list is fetched directly.
  // A failed fetch leaves the list as it is; the stream catches it up.
  function refreshList() {
    return fetch(location.href, { credentials: 'same-origin', cache: 'no-store' }).then(function (res) {
      if (!res.ok) {
        throw new Error('status ' + res.status);
      }
      return res.text();
    }).then(function (text) {
      var fresh = new DOMParser().parseFromString(text, 'text/html').getElementById(LIST_ID);
      var cur = document.getElementById(LIST_ID);
      if (!fresh || !cur) {
        return;
      }
      var hadFocus = cur.contains(document.activeElement);
      var node = document.importNode(fresh, true);
      cur.replaceWith(node);
      // The focused Remove link may be gone with its row, so focus moves
      // to the list's heading rather than falling back to the page's top.
      var head = hadFocus ? node.querySelector('h2') : null;
      if (head) {
        head.setAttribute('tabindex', '-1');
        head.focus({ preventScroll: true });
      }
    }).catch(function () {
      return null;
    });
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

  // stopThisDevice is Turn off: the server forgets this browser's
  // subscription (and clears the device cookie), then the browser drops it.
  // Removing this device from the list runs the same code, so the card and
  // the list can never disagree about whether it is signed up. The one
  // difference is a browser with no subscription left that the list still
  // marks (its permission was taken away, which drops the subscription
  // without telling afk): there is no endpoint to send, so the row's id is
  // removed by the remove address instead. Turn off passes no id; with no
  // subscription it has nothing to do.
  function stopThisDevice(id) {
    return currentSubscription().then(function (sub) {
      if (sub) {
        return post('/api/push/unsubscribe', { endpoint: sub.endpoint }).then(function () {
          return sub.unsubscribe();
        });
      }
      if (!id) {
        return null;
      }
      return post('/api/notifications/remove', { id: id, confirm: true }).then(function (r) {
        if (!r.ok) {
          throw new Error(r.data.error || 'afk could not remove this device. Try again.');
        }
      });
    });
  }

  function turnOff(btn) {
    var done = busy(btn, 'Turning off');
    stopThisDevice('').catch(function (err) {
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

  // lookUp asks the server whether this browser's subscription is still
  // listed (see afterLookup). Only a browser holding a subscription asks:
  // one without has nothing to look up and already shows Off. The endpoint
  // goes in the POST body only, never in the page or an address.
  function lookUp() {
    currentSubscription().then(function (sub) {
      if (!sub) {
        return null;
      }
      return post('/api/push/device', { endpoint: sub.endpoint }).then(function (r) {
        var marked = markedID();
        var next = afterLookup(r.ok ? r.data : null, marked);
        if (next === 'refresh') {
          return refreshList();
        }
        if (next !== 'off') {
          return null;
        }
        // The lookup cleared the device cookie, so a row still marked as
        // this device is redrawn unmarked.
        return sub.unsubscribe().catch(function () {
          return null;
        }).then(function () {
          return Promise.all([draw(), marked ? refreshList() : null]);
        });
      });
    }).catch(function () {
      // A failed lookup leaves the page as the server drew it.
      return null;
    });
  }

  document.addEventListener('click', function (ev) {
    var t = ev.target;
    var btn = t && t.closest ? t.closest('button') : null;
    if (!btn || btn.disabled || !card || !card.contains(btn)) {
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

  // A row's Remove opens the shared dialog filled for that row. The click
  // is delegated on the document because live.js replaces the list's rows.
  // A modified or middle click, or a browser without <dialog>, follows the
  // link to the no-JavaScript confirm page instead. Every word goes in with
  // textContent, so a device label is shown as text, never as markup.
  document.addEventListener('click', function (ev) {
    var link = ev.target && ev.target.closest ? ev.target.closest('a[data-push-remove]') : null;
    if (!link || ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) {
      return;
    }
    var sheet = document.getElementById(SHEET_ID);
    var title = document.getElementById(SHEET_ID + '-title');
    var body = document.getElementById(SHEET_ID + '-body');
    var form = document.getElementById(SHEET_ID + '-form');
    var input = form ? form.querySelector('input[name="id"]') : null;
    if (!sheet || typeof sheet.showModal !== 'function' || !title || !body || !input) {
      return;
    }
    ev.preventDefault();
    var thisDevice = link.hasAttribute('data-this-device');
    var text = removeSheetText(link.getAttribute('data-device-label') || '', thisDevice);
    title.textContent = text.title;
    var p = body.querySelector('p');
    if (!p) {
      p = document.createElement('p');
      body.appendChild(p);
    }
    p.textContent = text.body;
    input.value = link.getAttribute('data-device-id') || '';
    if (thisDevice) {
      sheet.setAttribute('data-this-device', '');
    } else {
      sheet.removeAttribute('data-this-device');
    }
    sheet.showModal();
    // Keep it starts focused (autofocus), set again here for browsers that
    // skip autofocus inside a dialog.
    var safe = sheet.querySelector('[autofocus]');
    if (safe) {
      safe.focus();
    }
  });

  // Remove in the dialog for this device runs Turn off instead of posting
  // the form. The listener is on the capture phase so it runs before
  // submit.js (which would post the form by fetch) and confirm.js (which
  // would put a spinner on the button) see the submit; stopping it there
  // keeps both out. For another device the submit goes on untouched: the
  // form posts, the server removes the entry and the page comes back with
  // its toast.
  document.addEventListener('submit', function (ev) {
    var form = ev.target;
    var sheet = document.getElementById(SHEET_ID);
    if (!form || form.id !== SHEET_ID + '-form' || !sheet || !sheet.hasAttribute('data-this-device')) {
      return;
    }
    ev.preventDefault();
    ev.stopPropagation();
    var btn = document.querySelector('button[form="' + SHEET_ID + '-form"]');
    if (btn && btn.disabled) {
      return;
    }
    var input = form.querySelector('input[name="id"]');
    var done = btn ? busy(btn, 'Removing') : function () {};
    stopThisDevice(input ? input.value : '').then(function () {
      say('Device removed.');
    }, function (err) {
      say(failure(err));
    }).then(function () {
      done();
      if (sheet.open) {
        sheet.close();
      }
      return Promise.all([draw(), refreshList()]);
    });
  }, true);

  document.addEventListener('afk:swapped', function () {
    if (findCard()) {
      draw();
    }
  });

  if (findCard()) {
    draw();
    lookUp();
  }
})();
