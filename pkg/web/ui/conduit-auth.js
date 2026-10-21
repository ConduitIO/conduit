/*
 * conduit-auth.js: API token support for the built-in UI.
 *
 * Served by the engine next to the embedded conduit-ui bundle and loaded
 * before it, so the bundle's own fetch() and WebSocket calls to the Conduit API
 * carry the API token without the bundle knowing about it.
 *
 *  - The token is typed into a prompt and kept in sessionStorage only (this tab,
 *    gone when it closes). Never localStorage, cookies or a URL.
 *  - HTTP: sent as `Authorization: Bearer <token>`.
 *  - WebSocket: the browser API cannot set headers, so the token travels as the
 *    subprotocol `conduit.token.<base64url(token)>` next to `conduit`. The
 *    server checks it in constant time and selects `conduit`.
 *  - A 401 clears the stored token and prompts again, then retries once.
 *
 * Only same-origin requests under /v1/ are touched.
 */
(function () {
  'use strict';

  var KEY = 'conduit.api.token';
  var memToken = ''; // fallback when sessionStorage is unavailable
  var origFetch = window.fetch.bind(window);
  var OrigWebSocket = window.WebSocket;
  var inflightPrompt = null;

  function getToken() {
    try {
      return window.sessionStorage.getItem(KEY) || '';
    } catch (e) {
      return memToken;
    }
  }
  function setToken(t) {
    memToken = t;
    try {
      window.sessionStorage.setItem(KEY, t);
    } catch (e) {
      /* kept in memory only */
    }
  }
  function clearToken() {
    memToken = '';
    try {
      window.sessionStorage.removeItem(KEY);
    } catch (e) {
      /* nothing to clear */
    }
  }

  function isAPI(target) {
    var u;
    try {
      u = new URL(String(target), window.location.href);
    } catch (e) {
      return false;
    }
    var loc = window.location;
    var scheme = u.protocol === 'ws:' ? 'http:' : u.protocol === 'wss:' ? 'https:' : u.protocol;
    return scheme === loc.protocol && u.host === loc.host && u.pathname.indexOf('/v1/') === 0;
  }

  function b64url(s) {
    var bytes = new TextEncoder().encode(s);
    var bin = '';
    for (var i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  }

  // defaultPrompt shows a modal with a password field and resolves with the
  // entered token, or '' if cancelled.
  function defaultPrompt() {
    return new Promise(function (resolve) {
      var doc = window.document;
      var overlay = doc.createElement('div');
      overlay.setAttribute('role', 'dialog');
      overlay.setAttribute('aria-modal', 'true');
      overlay.setAttribute('aria-label', 'Conduit API token');
      overlay.style.cssText =
        'position:fixed;inset:0;z-index:2147483647;display:flex;align-items:center;justify-content:center;background:rgba(0,0,0,.55);font:14px system-ui,sans-serif';
      var form = doc.createElement('form');
      form.style.cssText =
        'background:#fff;color:#111;padding:20px;border-radius:8px;min-width:320px;max-width:90vw;display:flex;flex-direction:column;gap:12px';
      var label = doc.createElement('label');
      label.textContent = 'This Conduit requires an API token.';
      var input = doc.createElement('input');
      input.type = 'password';
      input.autocomplete = 'off';
      input.spellcheck = false;
      input.setAttribute('aria-label', 'API token');
      input.style.cssText = 'padding:8px;border:1px solid #888;border-radius:4px;font:inherit';
      var row = doc.createElement('div');
      row.style.cssText = 'display:flex;gap:8px;justify-content:flex-end';
      var cancel = doc.createElement('button');
      cancel.type = 'button';
      cancel.textContent = 'Cancel';
      var ok = doc.createElement('button');
      ok.type = 'submit';
      ok.textContent = 'Use token';
      row.appendChild(cancel);
      row.appendChild(ok);
      form.appendChild(label);
      form.appendChild(input);
      form.appendChild(row);
      overlay.appendChild(form);

      function done(value) {
        overlay.remove();
        resolve(value);
      }
      form.addEventListener('submit', function (ev) {
        ev.preventDefault();
        done(input.value.trim());
      });
      cancel.addEventListener('click', function () {
        done('');
      });
      overlay.addEventListener('keydown', function (ev) {
        if (ev.key === 'Escape') done('');
      });
      doc.body.appendChild(overlay);
      input.focus();
    });
  }

  var hooks = { promptForToken: defaultPrompt };
  // Exposed so tests (and an embedding page that wants its own dialog) can
  // replace the prompt. It cannot read the token.
  window.__conduitAuth = hooks;

  // One dialog at a time: concurrent 401s share it.
  function promptOnce() {
    if (!inflightPrompt) {
      inflightPrompt = Promise.resolve(hooks.promptForToken()).then(
        function (t) {
          inflightPrompt = null;
          return t || '';
        },
        function () {
          inflightPrompt = null;
          return '';
        }
      );
    }
    return inflightPrompt;
  }

  function attempt(req, token) {
    if (token) req.headers.set('Authorization', 'Bearer ' + token);
    return origFetch(req);
  }

  function apiFetch(input, init) {
    var base = new Request(input, init);
    var spare = base.clone(); // a body can be read once; keep one for the retry
    var used = getToken();
    return attempt(base, used).then(function (resp) {
      if (resp.status !== 401) return resp;
      // Another request may already have replaced a rejected token.
      var current = getToken();
      var next = current && current !== used ? Promise.resolve(current) : (clearToken(), promptOnce());
      return next.then(function (tok) {
        if (!tok) return resp;
        setToken(tok);
        return attempt(spare, tok).then(function (r2) {
          if (r2.status === 401) clearToken();
          return r2;
        });
      });
    });
  }

  window.fetch = function (input, init) {
    var url = typeof input === 'string' ? input : input && input.url ? input.url : String(input);
    if (!isAPI(url)) return origFetch(input, init);
    return apiFetch(input, init);
  };

  var probing = false;
  function probe() {
    if (probing) return;
    probing = true;
    window
      .fetch('/v1/plugins')
      .catch(function () {})
      .then(function () {
        probing = false;
      });
  }

  function AuthedWebSocket(url, protocols) {
    var api = isAPI(url);
    var token = api ? getToken() : '';
    var ws;
    if (token) {
      var list = protocols === undefined ? [] : typeof protocols === 'string' ? [protocols] : Array.prototype.slice.call(protocols);
      list = ['conduit'].concat(
        list.filter(function (p) {
          return p !== 'conduit';
        })
      );
      list.push('conduit.token.' + b64url(token));
      ws = new OrigWebSocket(url, list);
    } else {
      ws = protocols === undefined ? new OrigWebSocket(url) : new OrigWebSocket(url, protocols);
    }
    if (api) {
      var opened = false;
      ws.addEventListener('open', function () {
        opened = true;
      });
      // A refused upgrade is invisible to scripts; a plain request tells us if it
      // was the token, and prompts if so.
      ws.addEventListener('close', function () {
        if (!opened) probe();
      });
    }
    return ws;
  }
  AuthedWebSocket.prototype = OrigWebSocket.prototype;
  ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED'].forEach(function (k) {
    AuthedWebSocket[k] = OrigWebSocket[k];
  });
  window.WebSocket = AuthedWebSocket;
})();
