// submit.js lets the page's forms work without a full reload, makes an inbox
// item's options a choice rather than an instant send, and keeps the
// "Updated ... ago" line counting. Every part is an enhancement: without this
// script the forms post natively, each option button sends its own answer,
// and the line says "Updated just now" as the server wrote it.
'use strict';

(function () {
  var TICK_MS = 5000;
  // TOAST_FILL_MS is how long a fresh toast waits empty before its words go
  // in. A status region that arrives already holding its text is often not
  // read out by a screen reader; one whose text changes after it is in the
  // page is.
  var TOAST_FILL_MS = 100;

  var canFetch = 'fetch' in window && 'DOMParser' in window && 'FormData' in window &&
    'URLSearchParams' in window && !!(window.history && history.pushState);
  // pushed is set once this script has put an address in the history. Going
  // back to an earlier entry then leaves the swapped page on screen with an
  // older address, so a back or forward press loads that address for real.
  var pushed = false;

  // ---- Answer form: options choose, Send answer sends ----

  function answerForms() {
    return Array.prototype.slice.call(document.querySelectorAll('form[data-answer-form]'));
  }

  // choiceInput is the hidden field that carries the chosen option. It is
  // made here rather than written in the template: with JavaScript off the
  // option buttons send their own name and value, and an empty hidden
  // "choice" in the form would be posted beside it.
  function choiceInput(form) {
    var input = form.querySelector('input[type="hidden"][name="choice"][data-choice]');
    if (!input) {
      input = document.createElement('input');
      input.type = 'hidden';
      input.name = 'choice';
      input.value = '';
      input.setAttribute('data-choice', '');
      form.appendChild(input);
    }
    return input;
  }

  // update enables Send answer only once there is something to send: a
  // chosen option or text that is not blank. The server would refuse an
  // empty answer anyway; this says so before the press instead of after.
  function update(form) {
    var send = form.querySelector('[data-send]');
    if (!send) {
      return;
    }
    var text = form.querySelector('textarea[name="answer"]');
    var typed = text && text.value.trim() !== '';
    var chosen = form.querySelector('[data-choice]');
    send.disabled = !(typed || (chosen && chosen.value !== ''));
  }

  function arm(form) {
    if (form.querySelector('[data-option]')) {
      choiceInput(form);
    }
    update(form);
  }

  function armAll() {
    answerForms().forEach(arm);
  }

  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest ? ev.target.closest('[data-option]') : null;
    if (!btn) {
      return;
    }
    var form = btn.closest('form[data-answer-form]');
    if (!form) {
      return;
    }
    // The option is a submit button so that it works with JavaScript off;
    // here it only marks the choice.
    ev.preventDefault();
    var input = choiceInput(form);
    var wasChosen = btn.classList.contains('is-chosen');
    Array.prototype.slice.call(form.querySelectorAll('[data-option]')).forEach(function (o) {
      o.classList.remove('is-chosen');
      o.setAttribute('aria-pressed', 'false');
    });
    if (wasChosen) {
      input.value = '';
    } else {
      btn.classList.add('is-chosen');
      btn.setAttribute('aria-pressed', 'true');
      input.value = btn.value;
    }
    update(form);
  });

  document.addEventListener('input', function (ev) {
    var form = ev.target && ev.target.closest ? ev.target.closest('form[data-answer-form]') : null;
    if (form) {
      update(form);
    }
  });

  // ---- "Updated ... ago" ----

  // The count starts when this page first sees a time, not from its
  // datetime: the server's clock and the phone's need not agree, and the
  // server writes the time of its own read, which is when the page (or the
  // swapped-in region) arrived.
  function tick() {
    var now = Date.now();
    Array.prototype.slice.call(document.querySelectorAll('time[data-updated]')).forEach(function (el) {
      var seen = parseInt(el.getAttribute('data-seen'), 10);
      if (isNaN(seen)) {
        seen = now;
        el.setAttribute('data-seen', String(now));
      }
      var s = Math.max(0, Math.floor((now - seen) / 1000));
      var text;
      if (s < 5) {
        text = 'Updated just now';
      } else if (s < 60) {
        text = 'Updated ' + s + ' s ago';
      } else if (s < 3600) {
        text = 'Updated ' + Math.floor(s / 60) + ' min ago';
      } else {
        text = 'Updated ' + Math.floor(s / 3600) + ' h ago';
      }
      if (el.textContent !== text) {
        el.textContent = text;
      }
    });
  }

  // ---- Posting a form without a reload ----

  function assets(doc) {
    return Array.prototype.slice.call(doc.querySelectorAll('link[rel="stylesheet"], script[src]'))
      .map(function (el) {
        return el.getAttribute('href') || el.getAttribute('src');
      }).join(' ');
  }

  // nativeSubmit posts the form the ordinary way, so the server's own page
  // shows: its error page for a refusal, or the result when the fetch could
  // not be made. A named submit button is copied into a hidden field first,
  // because form.submit() sends no submitter. The prototype's submit is
  // called because a control named "submit" would hide form.submit.
  function nativeSubmit(form, submitter) {
    if (submitter && submitter.name) {
      var copy = document.createElement('input');
      copy.type = 'hidden';
      copy.name = submitter.name;
      copy.value = submitter.value;
      form.appendChild(copy);
    }
    HTMLFormElement.prototype.submit.call(form);
  }

  // show puts the fetched page in place of the current one. It returns false
  // when it cannot, and the caller then loads the address for real: the post
  // has already been made, so it must not be sent again.
  function show(doc, url) {
    var fresh = doc.querySelector('.shell');
    var current = document.querySelector('.shell');
    // A different stylesheet or script address means afk was updated since
    // this page loaded; the new page needs its own files, not these.
    if (!fresh || !current || assets(doc) !== assets(document)) {
      return false;
    }
    Array.prototype.slice.call(document.querySelectorAll('dialog[open]')).forEach(function (d) {
      d.close();
    });
    var node = document.importNode(fresh, true);
    var toast = node.querySelector('[data-toast]');
    var words = toast ? toast.textContent : '';
    if (toast) {
      toast.textContent = '';
    }
    current.replaceWith(node);
    document.title = doc.title;
    if (url && url !== location.href) {
      history.pushState(null, '', url);
      pushed = true;
    }
    window.scrollTo(0, 0);
    var main = node.querySelector('main');
    if (main) {
      // Focus goes to the new content so a screen reader starts there, not
      // on a button that no longer exists.
      main.setAttribute('tabindex', '-1');
      main.focus({ preventScroll: true });
    }
    if (toast) {
      setTimeout(function () {
        toast.textContent = words;
      }, TOAST_FILL_MS);
    }
    armAll();
    tick();
    document.dispatchEvent(new CustomEvent('afk:swapped'));
    return true;
  }

  document.addEventListener('submit', function (ev) {
    var form = ev.target;
    if (!canFetch || ev.defaultPrevented || !form || !form.hasAttribute ||
        !form.hasAttribute('data-enhance') || (form.method || '').toLowerCase() !== 'post') {
      return;
    }
    ev.preventDefault();
    // One post at a time: a second press while the first is on its way is
    // dropped rather than sent twice.
    if (form.getAttribute('data-busy') === 'true') {
      return;
    }
    form.setAttribute('data-busy', 'true');
    var submitter = ev.submitter || null;
    var body;
    try {
      body = new URLSearchParams(submitter ? new FormData(form, submitter) : new FormData(form));
    } catch (err) {
      nativeSubmit(form, submitter);
      return;
    }
    // landed is the address the server's answer came from, once it has
    // answered with a page: from then on the post has been taken.
    var landed = '';
    fetch(form.action, { method: 'POST', body: body, credentials: 'same-origin', redirect: 'follow' })
      .then(function (res) {
        var type = res.headers.get('Content-Type') || '';
        if (!res.ok || type.indexOf('text/html') === -1) {
          throw new Error('status ' + res.status);
        }
        landed = res.url || location.href;
        return res.text().then(function (text) {
          var doc = new DOMParser().parseFromString(text, 'text/html');
          if (!show(doc, res.url)) {
            location.assign(landed);
          }
        });
      })
      .catch(function () {
        if (landed) {
          // The server took the post; only reading or showing its answer
          // failed. Load the page it sent us to instead of posting again.
          location.assign(landed);
          return;
        }
        nativeSubmit(form, submitter);
      });
  });

  window.addEventListener('popstate', function () {
    if (pushed) {
      location.reload();
    }
  });

  document.addEventListener('afk:live', tick);

  armAll();
  tick();
  setInterval(tick, TICK_MS);
})();
