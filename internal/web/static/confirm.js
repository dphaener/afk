// confirm.js opens the page's confirm sheets and hides the toast. A link with
// data-confirm="<dialog id>" opens that dialog instead of going to its
// no-JavaScript confirm page; without this script the link simply goes there.
'use strict';

(function () {
  var TOAST_MS = 5000;

  function dialogFor(link) {
    var d = document.getElementById(link.getAttribute('data-confirm'));
    return d && typeof d.showModal === 'function' ? d : null;
  }

  document.addEventListener('click', function (ev) {
    var link = ev.target.closest ? ev.target.closest('a[data-confirm]') : null;
    if (!link || ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) {
      return;
    }
    var d = dialogFor(link);
    if (!d) {
      return;
    }
    ev.preventDefault();
    d.showModal();
    // The safe button is the one that starts focused (autofocus), set again
    // here for browsers that skip autofocus inside a dialog.
    var safe = d.querySelector('[autofocus]');
    if (safe) {
      safe.focus();
    }
  });

  // Escape closes a modal dialog natively and the safe button is a
  // method="dialog" form, so neither needs code here.

  document.addEventListener('submit', function (ev) {
    var form = ev.target;
    if (!form || !form.closest || !form.closest('dialog.confirm-sheet') || form.method === 'dialog') {
      return;
    }
    var btn = form.querySelector('button[type="submit"]');
    if (!btn || btn.classList.contains('is-loading')) {
      return;
    }
    // Keep the button's width, so the new label does not shift the row.
    btn.style.width = btn.getBoundingClientRect().width + 'px';
    var label = btn.getAttribute('data-loading-label') || btn.textContent;
    btn.classList.add('is-loading');
    btn.setAttribute('aria-busy', 'true');
    var spin = document.createElement('span');
    spin.className = 'spinner';
    spin.setAttribute('aria-hidden', 'true');
    btn.textContent = '';
    btn.appendChild(spin);
    btn.appendChild(document.createTextNode(label));
  });

  var toastTimer = null;

  function armToast() {
    if (toastTimer !== null) {
      clearTimeout(toastTimer);
      toastTimer = null;
    }
    var toast = document.querySelector('[data-toast]');
    if (!toast) {
      return;
    }
    toastTimer = setTimeout(function () {
      toastTimer = null;
      var region = toast.parentNode;
      (region && region.classList.contains('toast-region') ? region : toast).hidden = true;
    }, TOAST_MS);
  }

  armToast();
  // A page swapped in by submit.js brings its own toast, timed afresh.
  document.addEventListener('afk:swapped', armToast);
})();
