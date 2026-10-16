// chat.js makes the planning chat's forms send without leaving the page: the
// reply goes to the JSON API, the owner's words appear at once as a pending
// message, and the live swap later replaces them with the real thread. Every
// part is an enhancement: without fetch (or without this script) the forms
// post natively to the page's own address and the server draws the result.
'use strict';

// stalePending says whether a pending owner message can be dropped because
// the thread now shows it. sentCount is how many owner messages the thread
// held (plus earlier pending ones) when this one was sent; nowCount is how
// many it holds now. Any growth past sentCount means the server wrote this
// reply, whether it was typed or a choice. Counting is needed because a
// choice is shown by its label while the server writes "Header: Label", so
// the words never match. The text match stays as a second way for a typed
// reply. It is a pure function so a node test can load this file.
function stalePending(sentCount, nowCount, pendingText, ownerTexts) {
  if (nowCount > sentCount) {
    return true;
  }
  return ownerTexts.indexOf(pendingText) !== -1;
}

if (typeof module !== 'undefined') {
  module.exports = { stalePending: stalePending };
}

(function () {
  // Loaded by a node test: there is no page to enhance.
  if (typeof document === 'undefined' || typeof window === 'undefined') {
    return;
  }

  // Without fetch there is nothing to enhance; the forms post natively.
  if (!('fetch' in window)) {
    return;
  }

  var NETWORK_MESSAGE = 'Could not reach afk. Your reply was not sent; try again.';

  function forms() {
    return Array.prototype.slice.call(document.querySelectorAll('form[data-chat-form]'));
  }

  // replyBox is the one box the form's Send reads: the composer's text, or
  // the own-answer box beside the option buttons. A form of radio buttons
  // and checkboxes has none.
  function replyBox(form) {
    return form.querySelector('textarea[name="text"], textarea[name="other"]');
  }

  function sendButton(form) {
    return form.querySelector('button[type="submit"]:not([data-chat-option])');
  }

  // arm keeps Send disabled while the box is blank. The server would refuse
  // an empty reply (and a blank own answer would decline the agent's
  // question), so this says so before the press. A box the server drew
  // disabled stays disabled: that is the "answer the question above" state.
  function arm(form) {
    var box = replyBox(form);
    var send = sendButton(form);
    if (!box || !send || box.disabled) {
      return;
    }
    send.disabled = box.value.trim() === '';
  }

  function armAll() {
    forms().forEach(arm);
  }

  document.addEventListener('input', function (ev) {
    var form = ev.target && ev.target.closest ? ev.target.closest('form[data-chat-form]') : null;
    if (form) {
      arm(form);
    }
  });

  // ---- Building the reply ----

  // checked lists, per question in page order, the labels picked in a form
  // of radio buttons and checkboxes. Each fieldset's inputs share a name
  // "choice-N"; the names are kept in order of first appearance.
  function checked(form) {
    var names = [];
    var picks = {};
    Array.prototype.slice.call(form.querySelectorAll('input[type="radio"], input[type="checkbox"]'))
      .forEach(function (input) {
        if (names.indexOf(input.name) === -1) {
          names.push(input.name);
          picks[input.name] = [];
        }
        if (input.checked) {
          picks[input.name].push(input.value);
        }
      });
    return names.map(function (n) {
      return picks[n];
    });
  }

  // reply returns the JSON body for a press, and the words to show as the
  // owner's pending message, or null when there is nothing to send.
  function reply(form, submitter) {
    if (submitter && submitter.hasAttribute('data-chat-option')) {
      return { body: { choices: [[submitter.value]] }, shown: submitter.value, option: submitter };
    }
    var picks = checked(form);
    if (picks.length > 0) {
      var shown = picks.map(function (p) {
        return p.join(', ');
      }).join('; ');
      return { body: { choices: picks }, shown: shown, option: null };
    }
    var box = replyBox(form);
    if (!box || box.value.trim() === '') {
      return null;
    }
    var key = box.name === 'other' ? 'other' : 'text';
    var body = {};
    body[key] = box.value;
    return { body: body, shown: box.value, option: null };
  }

  // ---- The form's state while a post is on its way ----

  function errorLine(form) {
    return form.querySelector('[data-chat-error]');
  }

  function showError(form, words) {
    var line = errorLine(form);
    if (line) {
      line.textContent = words;
    }
  }

  // lock stops a second press while the first is on its way, and marks the
  // pressed option so the card shows what was sent. unlock undoes it when
  // the reply was not taken, so the owner can fix it and press again.
  function controls(form) {
    return Array.prototype.slice.call(form.querySelectorAll('button, input, textarea'));
  }

  function lock(form, option) {
    form.setAttribute('data-busy', 'true');
    controls(form).forEach(function (c) {
      c.setAttribute('data-was-disabled', c.disabled ? 'true' : 'false');
      c.disabled = true;
    });
    if (option) {
      option.classList.add('is-chosen');
      option.setAttribute('aria-pressed', 'true');
    }
  }

  function unlock(form, option) {
    form.removeAttribute('data-busy');
    controls(form).forEach(function (c) {
      c.disabled = c.getAttribute('data-was-disabled') === 'true';
      c.removeAttribute('data-was-disabled');
    });
    if (option) {
      option.classList.remove('is-chosen');
      option.setAttribute('aria-pressed', 'false');
    }
    arm(form);
  }

  // ---- The pending owner message ----

  // pendingBox is the container for messages sent but not yet in the thread.
  // It sits just after the thread and outside every live region, so a live
  // swap of the thread cannot remove a message before the agent's page
  // shows it.
  function pendingBox() {
    var box = document.getElementById('chat-pending');
    if (box) {
      return box;
    }
    var thread = document.getElementById('chat-thread');
    if (!thread || !thread.parentNode) {
      return null;
    }
    box = document.createElement('div');
    box.id = 'chat-pending';
    box.className = 'thread chat-pending';
    thread.parentNode.insertBefore(box, thread.nextSibling);
    return box;
  }

  // addPending shows the words at once. Only textContent is used: the words
  // are the owner's own, but they must never become markup.
  function addPending(words) {
    var box = pendingBox();
    if (!box) {
      return;
    }
    var msg = document.createElement('div');
    msg.className = 'msg msg--owner';
    msg.setAttribute('data-pending-text', words.trim());
    // Owner messages the thread holds now, plus those still pending ahead of
    // this one: this reply is the next to arrive after all of them.
    var thread = document.getElementById('chat-thread');
    var held = thread ? thread.querySelectorAll('.msg--owner').length : 0;
    msg.setAttribute('data-sent-count', String(held + box.querySelectorAll('[data-pending-text]').length));
    var who = document.createElement('span');
    who.className = 'msg__who';
    who.textContent = 'You';
    var bubble = document.createElement('div');
    bubble.className = 'msg__bubble item-body';
    bubble.textContent = words;
    var meta = document.createElement('span');
    meta.className = 'meta chat-sent';
    meta.textContent = 'Sent';
    msg.appendChild(who);
    msg.appendChild(bubble);
    msg.appendChild(meta);
    box.appendChild(msg);
  }

  // prune drops each pending message once the thread shows its reply (see
  // stalePending) and keeps the rest.
  function prune() {
    var box = document.getElementById('chat-pending');
    var thread = document.getElementById('chat-thread');
    if (!box || !thread) {
      return;
    }
    var now = thread.querySelectorAll('.msg--owner').length;
    var seen = Array.prototype.slice.call(thread.querySelectorAll('.msg--owner .msg__bubble'))
      .map(function (b) {
        return b.textContent.trim();
      });
    Array.prototype.slice.call(box.querySelectorAll('[data-pending-text]')).forEach(function (m) {
      var sent = parseInt(m.getAttribute('data-sent-count'), 10);
      if (stalePending(isNaN(sent) ? now : sent, now, m.getAttribute('data-pending-text'), seen)) {
        m.remove();
      }
    });
  }

  document.addEventListener('afk:live', prune);

  // ---- Sending ----

  document.addEventListener('submit', function (ev) {
    var form = ev.target;
    if (!form || !form.hasAttribute || !form.hasAttribute('data-chat-form')) {
      return;
    }
    var project = form.getAttribute('data-project');
    var what = project ? reply(form, ev.submitter || null) : null;
    if (!what) {
      // Nothing to send, or no project to send to: leave it to the server,
      // which says in words what is missing.
      return;
    }
    ev.preventDefault();
    if (form.getAttribute('data-busy') === 'true') {
      return;
    }
    showError(form, '');
    var box = replyBox(form);
    lock(form, what.option);
    fetch('/api/p/' + encodeURIComponent(project) + '/plan/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(what.body),
      credentials: 'same-origin'
    })
      .then(function (res) {
        if (res.ok) {
          addPending(what.shown);
          // A choice card stays locked and marked; a text box is emptied.
          if (!what.option) {
            unlock(form, null);
            if (box) {
              box.value = '';
            }
            arm(form);
          } else {
            form.removeAttribute('data-busy');
          }
          return;
        }
        return res.json().then(function (data) {
          return data && data.error ? data.error : 'afk did not take that reply.';
        }, function () {
          return 'afk did not take that reply.';
        }).then(function (words) {
          unlock(form, what.option);
          showError(form, words);
        });
      })
      .catch(function () {
        unlock(form, what.option);
        showError(form, NETWORK_MESSAGE);
      });
  });

  // Live swaps bring in fresh forms; arm them and drop pending messages the
  // thread now shows.
  document.addEventListener('afk:live', armAll);
  document.addEventListener('afk:swapped', armAll);
  armAll();
})();
