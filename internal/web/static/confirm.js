// confirm.js opens the page's confirm sheets and hides the toast. A link with
// data-confirm="<dialog id>" opens that dialog instead of going to its
// no-JavaScript confirm page; without this script the link simply goes there.
//
// A task's Set status… sheet (data-status-sheet) holds one block of
// sentences per status, written by the server; this script only picks which
// one shows, puts the typed replacing ids into superseded's, and sets the
// action button's label from the checked radio. It writes no words of its
// own, so the sheet says what the no-JavaScript page says.
//
// A form with data-confirm-form="<dialog id>" (Start a new autonomous
// session…) opens that dialog instead of going to its no-JavaScript confirm
// page. The dialog's hidden fields marked data-fill-field get the form's
// values, and an element with data-fill-text gets that attribute's text
// (the server's words) with {name}, {description} and {folder} replaced by
// them.
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
    if (d.hasAttribute('data-status-sheet')) {
      syncStatusSheet(d);
    }
    openSheet(d);
  });

  function openSheet(d) {
    d.showModal();
    // The safe button is the one that starts focused (autofocus), set again
    // here for browsers that skip autofocus inside a dialog.
    var safe = d.querySelector('[autofocus]');
    if (safe) {
      safe.focus();
    }
  }

  // A remove screen's radios carry the project's folder in data-folder; the
  // chosen one fills {folder} as well as {name}.
  // fillSheet copies the form's values into the dialog. The text is set with
  // textContent, so a typed value is shown as text and never as markup, and
  // the replacement is one pass, so a value holding "{name}" stays as typed.
  function fillSheet(d, form) {
    var values = {};
    Array.prototype.slice.call(form.elements).forEach(function (el) {
      if (!el.name) {
        return;
      }
      // Only the chosen radio or ticked box counts; every other radio of the
      // group would otherwise overwrite it with its own value.
      if ((el.type === 'radio' || el.type === 'checkbox') && !el.checked) {
        return;
      }
      values[el.name] = el.value.trim();
      if (el.hasAttribute('data-folder')) {
        values.folder = el.getAttribute('data-folder');
      }
    });
    Array.prototype.slice.call(d.querySelectorAll('[data-fill-field]')).forEach(function (input) {
      var v = values[input.getAttribute('data-fill-field')];
      input.value = v === undefined ? '' : v;
    });
    Array.prototype.slice.call(d.querySelectorAll('[data-fill-text]')).forEach(function (el) {
      el.textContent = el.getAttribute('data-fill-text').replace(/\{(name|description)\}|\{(folder)\}/g, function (all, key, folderKey) {
        key = key || folderKey;
        return Object.prototype.hasOwnProperty.call(values, key) ? values[key] : all;
      });
    });
  }

  // The browser has already checked the form's required and pattern rules
  // when submit fires, so only a form it accepted opens the sheet.
  document.addEventListener('submit', function (ev) {
    var form = ev.target;
    if (ev.defaultPrevented || !form || !form.hasAttribute || !form.hasAttribute('data-confirm-form')) {
      return;
    }
    var d = document.getElementById(form.getAttribute('data-confirm-form'));
    if (!d || typeof d.showModal !== 'function') {
      return;
    }
    ev.preventDefault();
    fillSheet(d, form);
    openSheet(d);
  });

  // Escape closes a modal dialog natively and the safe button is a
  // method="dialog" form, so neither needs code here.

  // splitIDs splits the replacing ids as taskstatus.SplitIDs does: on commas
  // and white space, empty pieces dropped.
  function splitIDs(text) {
    return text.split(/[\s,]+/).filter(function (s) {
      return s !== '';
    });
  }

  // syncStatusSheet makes the sheet match its checked radio.
  function syncStatusSheet(d) {
    var picked = d.querySelector('input[type="radio"][name="status"]:checked');
    var status = picked ? picked.value : '';
    var superseded = status === 'superseded';
    var field = d.querySelector('[data-ids-field]');
    var input = field ? field.querySelector('input') : null;
    if (field) {
      field.hidden = !superseded;
    }
    // A hidden field's typed ids must not be posted with another status:
    // the command refuses replacing ids for anything but superseded.
    if (input) {
      input.disabled = !superseded;
    }
    var ids = input && superseded ? splitIDs(input.value) : [];
    Array.prototype.slice.call(d.querySelectorAll('[data-sentence]')).forEach(function (block) {
      block.hidden = block.getAttribute('data-sentence') !== status;
    });
    Array.prototype.slice.call(d.querySelectorAll('[data-ids-template]')).forEach(function (p) {
      p.textContent = ids.length ?
        p.getAttribute('data-ids-template').split('{ids}').join(ids.join(', ')) :
        p.getAttribute('data-ids-default');
    });
    var empty = d.querySelector('[data-ids-empty]');
    if (empty) {
      empty.hidden = !(superseded && ids.length === 0);
    }
    var action = d.querySelector('[data-status-action]');
    if (action && !action.classList.contains('is-loading')) {
      if (picked) {
        action.textContent = picked.getAttribute('data-action-label');
      }
      // Superseded with no ids would only be refused; the sentence above
      // says what to type instead.
      action.disabled = superseded && ids.length === 0;
    }
  }

  function onSheetInput(ev) {
    var d = ev.target && ev.target.closest ? ev.target.closest('dialog[data-status-sheet]') : null;
    if (d) {
      syncStatusSheet(d);
    }
  }

  document.addEventListener('change', onSheetInput);
  document.addEventListener('input', onSheetInput);

  document.addEventListener('submit', function (ev) {
    var form = ev.target;
    if (!form || !form.closest || !form.closest('dialog.confirm-sheet') || form.method === 'dialog') {
      return;
    }
    // The status sheet's action button sits outside its form (form=), so
    // it is looked for by the form's id when the form holds none.
    var btn = form.querySelector('button[type="submit"]') ||
      (form.id ? document.querySelector('button[type="submit"][form="' + form.id + '"]') : null);
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
