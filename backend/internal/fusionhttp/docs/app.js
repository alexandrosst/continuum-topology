'use strict';
// The page is built from /api/v1/fusion/openapi.json. Nothing in it is typed twice: add a route or a parameter on the
// server and it shows up here. All text from the description goes in with textContent.
(function () {
  var SPEC = null, TOKEN = '';
  var $ = function (id) { return document.getElementById(id); };
  var el = function (tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  try { TOKEN = sessionStorage.getItem('fusion-docs-token') || ''; } catch (e) { /* storage can be blocked */ }
  // The theme the person chose in the app (the same key, so the two agree); with none chosen the system's preference decides, in CSS.
  try { var th = localStorage.getItem('ikhnos:theme'); if (th === 'light' || th === 'dark') document.documentElement.setAttribute('data-theme', th); } catch (e) { /* storage can be blocked */ }

  // A very small inline renderer for the description: paragraphs, **bold** and `code`.
  function inline(parent, text) {
    var re = /(\*\*[^*]+\*\*|`[^`]+`)/g, last = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > last) parent.appendChild(document.createTextNode(text.slice(last, m.index)));
      var t = m[0];
      parent.appendChild(t[0] === '*' ? el('strong', null, t.slice(2, -2)) : el('code', null, t.slice(1, -1)));
      last = m.index + t.length;
    }
    if (last < text.length) parent.appendChild(document.createTextNode(text.slice(last)));
  }
  function para(parent, text) { text.split(/\n\n+/).forEach(function (p) { var e = el('p'); inline(e, p); parent.appendChild(e); }); }

  function authHeaders(extra) {
    var h = Object.assign({}, extra || {});
    if (TOKEN) h['Authorization'] = 'Bearer ' + TOKEN;
    return h;
  }

  // ---- who am I ----
  function checkWho() {
    var who = $('who');
    who.className = 'who'; who.textContent = 'checking…';
    fetch('status', { headers: authHeaders({ Accept: 'application/json' }), credentials: 'same-origin' }).then(function (r) {
      return r.json().then(function (j) { return { r: r, j: j }; });
    }).then(function (x) {
      if (!x.r.ok) { who.className = 'who bad'; who.textContent = (x.j && x.j.error) || ('HTTP ' + x.r.status); return; }
      var a = x.j.access || {}, s = (a.signals || []).join(', ') || 'nothing';
      var scope = [];
      if (a.namespaces && a.namespaces.length) scope.push('namespaces ' + a.namespaces.join(', '));
      if (a.clusters && a.clusters.length) scope.push('clusters ' + a.clusters.join(', '));
      who.className = 'who ok';
      who.textContent = (a.kind === 'token' ? 'token “' + a.name + '”' : 'signed in as ' + a.name) + ' — reads ' + s + (scope.length ? ' (' + scope.join('; ') + ')' : '');
    }).catch(function () { who.className = 'who bad'; who.textContent = 'could not reach the server'; });
  }

  // ---- parameter inputs ----
  function makeInput(p) {
    var s = p.schema || {}, wrap = el('div');
    var isList = s.type === 'array', item = isList ? (s.items || {}) : s;
    wrap.get = function () { return ''; };
    if (p['x-multiline']) {
      var ta2 = el('textarea'); ta2.rows = 2; ta2.spellcheck = false; ta2.style.minHeight = '0';
      if (p.example != null) ta2.placeholder = String(p.example);
      wrap.appendChild(ta2); wrap.get = function () { return ta2.value; };
    } else if (item.enum && isList) {
      var boxes = item.enum.map(function (v) {
        var l = el('label', 'opt'), c = el('input'); c.type = 'checkbox'; c.value = v;
        l.appendChild(c); l.appendChild(document.createTextNode(v)); wrap.appendChild(l); return c;
      });
      wrap.get = function () { return boxes.filter(function (c) { return c.checked; }).map(function (c) { return c.value; }).join(','); };
    } else if (item.enum || item.type === 'boolean') {
      var sel = el('select'); sel.appendChild(el('option', null, '')).value = '';
      (item.enum || ['true', 'false']).forEach(function (v) { var o = el('option', null, v); o.value = v; sel.appendChild(o); });
      wrap.appendChild(sel); wrap.get = function () { return sel.value; };
    } else {
      var inp = el('input'); inp.type = 'text'; inp.spellcheck = false;
      if (p.example != null) inp.placeholder = String(p.example);
      if (item.type === 'integer') inp.inputMode = 'numeric';
      wrap.appendChild(inp); wrap.get = function () { return inp.value.trim(); };
    }
    return wrap;
  }

  // ---- parameters that make others do nothing ----
  // A parameter that overrides others (x-excludes: `query` is sent to Loki as written, so the filters would not be used) or that
  // needs a fused read (x-needs-fused) greys the ones it makes useless out while it is filled in. A control that already has a
  // value is never disabled, so two can not lock each other; the server refuses a request that carries both.
  function greyOut(inputs) {
    var byName = {};
    inputs.forEach(function (x) { byName[x.p.name] = x; x.raw = x.inp.get; });
    function filled(n) { return byName[n] && byName[n].raw() !== ''; }
    function refresh() {
      var fused = filled('fused') ? byName.fused.raw() === 'true' : (filled('include') || filled('promql'));
      inputs.forEach(function (x) {
        var why = '';
        var blockers = (x.p['x-excludes'] || []).filter(filled);
        if (blockers.length) why = 'ignored while ' + blockers.join(', ') + ' is set';
        else if (x.p['x-needs-fused'] && !fused) why = 'only applies to a fused read: set fused=true or an include list';
        var off = why !== '' && x.raw() === '';
        x.tr.className = off ? 'off' : '';
        x.tr.title = off ? why : '';
        Array.prototype.forEach.call(x.inp.querySelectorAll('input,select,textarea'), function (c) { c.disabled = off; });
        x.inp.get = off ? function () { return ''; } : x.raw;
      });
    }
    inputs.forEach(function (x) {
      Array.prototype.forEach.call(x.inp.querySelectorAll('input,select,textarea'), function (c) {
        c.addEventListener('input', refresh); c.addEventListener('change', refresh);
      });
    });
    refresh();
  }

  // ---- one route ----
  function renderOp(path, method, op) {
    var d = el('details', 'op'); d.id = 'op-' + op.operationId;
    var sum = el('summary');
    sum.appendChild(el('span', 'method ' + method, method.toUpperCase()));
    sum.appendChild(el('code', 'path', path.replace('/api/v1/fusion', '')));
    sum.appendChild(el('span', 'sum', op.summary || ''));
    d.appendChild(sum);
    var body = el('div', 'body'); d.appendChild(body);
    var link = el('a', 'permalink', '# link'); link.href = '#' + d.id; link.title = 'A link to this route'; body.appendChild(link);
    if (op.description) para(body, op.description);

    var inputs = [];
    var params = op.parameters || [];
    if (params.length) {
      var tb = el('table'), head = el('tr');
      ['Parameter', 'Value', 'Description'].forEach(function (h) { head.appendChild(el('th', null, h)); });
      tb.appendChild(head);
      params.forEach(function (p) {
        var tr = el('tr'), n = el('td', 'pn'), s = p.schema || {};
        n.appendChild(el('code', null, p.name));
        if (p.required) n.appendChild(el('span', 'err', ' *'));
        tr.appendChild(n);
        var inp = makeInput(p), td = el('td', 'pi'); td.appendChild(inp); tr.appendChild(td);
        var dd = el('td'); inline(dd, p.description || '');
        var meta = [(s.type === 'array' ? 'list of ' + ((s.items || {}).type || 'string') : s.type)];
        if (p['x-default']) meta.push('default ' + p['x-default']);
        dd.appendChild(el('div', 'meta', meta.join(' · ')));
        tr.appendChild(dd); tb.appendChild(tr);
        inputs.push({ p: p, inp: inp, tr: tr });
      });
      body.appendChild(tb);
      greyOut(inputs);
    }
    var ta = null;
    if (op.requestBody) {
      body.appendChild(el('h3', null, 'Request body (JSON)'));
      ta = el('textarea'); ta.spellcheck = false;
      var ex = op.requestBody.content['application/json'].example;
      ta.value = ex ? JSON.stringify(ex, null, 2) : '{}';
      body.appendChild(ta);
    }
    var acts = el('div', 'actions'), go = el('button', null, 'Send'), curl = el('button', 'ghost', 'Copy as curl');
    var stat = el('span', 'status'); acts.appendChild(go); acts.appendChild(curl); acts.appendChild(stat); body.appendChild(acts);
    var out = el('div'); body.appendChild(out);

    function build() {
      var url = path, qs = [];
      for (var i = 0; i < inputs.length; i++) {
        var v = inputs[i].inp.get(), p = inputs[i].p;
        if (p['in'] === 'path') { if (!v) throw new Error(p.name + ' is required'); url = url.replace('{' + p.name + '}', encodeURIComponent(v)); }
        else if (p['x-multiline']) v.split('\n').forEach(function (line) { if (line.trim()) qs.push(encodeURIComponent(p.name) + '=' + encodeURIComponent(line.trim())); });
        else if (v !== '') qs.push(encodeURIComponent(p.name) + '=' + encodeURIComponent(v));
      }
      return { url: url + (qs.length ? '?' + qs.join('&') : ''), qs: qs };
    }
    function wantsStream() { return inputs.some(function (x) { return x.p.name === 'stream' && x.inp.get() === 'true'; }); }

    curl.onclick = function () {
      try {
        var b = build(), c = "curl -sS" + (wantsStream() ? " -N" : "") + (method === 'post' ? " -X POST -H 'Content-Type: application/json' -d '" + (ta ? ta.value.replace(/'/g, "'\\''") : '{}') + "'" : '') +
          ' -H "Authorization: Bearer $FUSION_TOKEN" \'' + (location.origin + b.url).replace(/'/g, "'\\''") + "'";
        if (navigator.clipboard) navigator.clipboard.writeText(c).then(function () { stat.className = 'status'; stat.textContent = 'curl copied'; });
        else { out.textContent = ''; var pre = el('pre', 'out', c); out.appendChild(pre); }
      } catch (e) { stat.className = 'status bad'; stat.textContent = e.message; }
    };

    go.onclick = function () {
      var b; out.textContent = '';
      try { b = build(); } catch (e) { stat.className = 'status bad'; stat.textContent = e.message; return; }
      var init = { method: method.toUpperCase(), headers: authHeaders({ Accept: wantsStream() ? 'application/x-ndjson' : 'application/json' }), credentials: 'same-origin' };
      if (ta) {
        try { JSON.parse(ta.value); } catch (e) { stat.className = 'status bad'; stat.textContent = 'the body is not valid JSON'; return; }
        init.body = ta.value; init.headers['Content-Type'] = 'application/json';
      }
      go.disabled = true; stat.className = 'status'; stat.textContent = 'sending…';
      var t0 = performance.now();
      fetch(b.url, init).then(function (r) {
        var ct = r.headers.get('Content-Type') || '';
        var done = function (size) {
          stat.className = 'status ' + (r.ok ? 'ok' : 'bad');
          stat.textContent = r.status + ' ' + (r.statusText || '') + ' · ' + Math.round(performance.now() - t0) + ' ms' + (size != null ? ' · ' + fmtBytes(size) : '');
          go.disabled = false;
        };
        if (ct.indexOf('application/x-ndjson') >= 0 && r.body && r.body.getReader) return streamLines(r, out, t0, stat).then(done);
        return r.text().then(function (txt) {
          var shown = txt;
          try { shown = JSON.stringify(JSON.parse(txt), null, 2); } catch (e) { /* not JSON: show as is */ }
          showText(out, shown);
          done(txt.length);
        });
      }).catch(function (e) { stat.className = 'status bad'; stat.textContent = 'failed: ' + e.message; go.disabled = false; });
    };
    return d;
  }

  var SHOW_LIMIT = 200000;
  function showText(out, text) {
    var pre = el('pre', 'out', text.length > SHOW_LIMIT ? text.slice(0, SHOW_LIMIT) : text);
    out.appendChild(pre);
    if (text.length > SHOW_LIMIT) {
      var more = el('button', 'ghost', 'Show all ' + fmtBytes(text.length));
      more.onclick = function () { pre.textContent = text; more.remove(); };
      out.appendChild(more);
    }
  }
  function fmtBytes(n) { return n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KiB' : (n / 1048576).toFixed(1) + ' MiB'; }

  // NDJSON: each line is shown the moment it arrives, with how long after the request it did.
  function streamLines(r, out, t0, stat) {
    var reader = r.body.getReader(), dec = new TextDecoder(), buf = '', n = 0;
    function addLine(s) {
      if (!s.trim()) return;
      var row = el('div', 'line');
      row.appendChild(el('span', 't', '+' + Math.round(performance.now() - t0) + ' ms'));
      var shown = s;
      try { var o = JSON.parse(s); shown = o.type === 'result' && o.trace ? '{"type":"result","id":"' + o.id + '","status":' + o.status + ',"trace":{…' + (o.trace.spanCount || 0) + ' spans, ' + fmtBytes(s.length) + '}}' : s.length > 600 ? s.slice(0, 600) + '…' : s; } catch (e) { /* keep raw */ }
      var c = el('code', null, shown);
      c.title = 'Click for the whole line'; c.style.cursor = 'pointer';
      c.onclick = function () { try { c.textContent = JSON.stringify(JSON.parse(s), null, 2); c.style.whiteSpace = 'pre-wrap'; } catch (e) { c.textContent = s; } };
      row.appendChild(c); out.appendChild(row); n++;
      stat.className = 'status'; stat.textContent = n + ' lines…';
    }
    function pump() {
      return reader.read().then(function (x) {
        if (x.done) { addLine(buf); return null; }
        buf += dec.decode(x.value, { stream: true });
        var parts = buf.split('\n'); buf = parts.pop();
        parts.forEach(addLine);
        return pump();
      });
    }
    return pump();
  }

  // ---- schemas ----
  function resolve(s) {
    var seen = 0;
    while (s && s.$ref && seen++ < 8) s = SPEC.components.schemas[s.$ref.split('/').pop()];
    return s || {};
  }
  function typeName(s) {
    if (s.$ref) return s.$ref.split('/').pop();
    if (s.type === 'array') return '[' + typeName(s.items || {}) + ']';
    if (s.enum) return s.enum.join(' | ');
    return s.type || (s.allOf ? 'object' : '');
  }
  function renderNode(parent, name, s, depth, stack) {
    var node = el('div', 'node'), r = resolve(s);
    var head = el('div');
    if (name) head.appendChild(el('span', 'k', name));
    head.appendChild(el('span', 'ty', typeName(s)));
    if (r.description || s.description) { var d = el('span', 'meta'); d.textContent = ' — ' + (s.description || r.description); head.appendChild(d); }
    node.appendChild(head);
    parent.appendChild(node);
    var ref = s.$ref ? s.$ref : (s.items && s.items.$ref) || null;
    if (depth > 6 || (ref && stack.indexOf(ref) >= 0)) return;
    var props = {}, subs = [r];
    if (r.allOf) subs = r.allOf.map(resolve);
    var target = r.type === 'array' ? resolve(r.items || {}) : r;
    if (r.type === 'array' && target.type !== 'object' && !target.allOf) return;
    if (target.allOf) subs = target.allOf.map(resolve); else subs = [target];
    subs.forEach(function (x) { Object.assign(props, x.properties || {}); });
    var next = ref ? stack.concat([ref]) : stack;
    Object.keys(props).forEach(function (k) { renderNode(node, k, props[k], depth + 1, next); });
  }
  function renderSchemas() {
    var list = $('schemaList');
    Object.keys(SPEC.components.schemas).forEach(function (n) {
      if (n === 'Error' || n === 'Point') return;
      var d = el('details', 'schema'); d.id = 'schema-' + n;
      d.appendChild(el('summary', null, n));
      var tree = el('div', 'tree'); d.appendChild(tree);
      var done = false;
      d.addEventListener('toggle', function () { if (d.open && !done) { done = true; renderNode(tree, '', { $ref: '#/components/schemas/' + n }, 0, []); } });
      list.appendChild(d);
    });
  }

  // ---- page ----
  var ENTRIES = [], GROUPS = [];
  var slug = function (t) { return t.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, ''); };
  var short = function (path) { return path.replace('/api/v1/fusion', ''); };

  function render() {
    document.title = SPEC.info.title;
    $('title').textContent = SPEC.info.title;
    $('version').textContent = 'v' + SPEC.info.version;
    para($('intro'), SPEC.info.description || '');
    var nav = $('nav'), ops = $('ops'), byTag = {};
    var narrow = window.matchMedia && window.matchMedia('(max-width:800px)').matches;
    Object.keys(SPEC.paths).forEach(function (path) {
      Object.keys(SPEC.paths[path]).forEach(function (method) {
        var op = SPEC.paths[path][method], t = (op.tags || ['Other'])[0];
        (byTag[t] = byTag[t] || []).push({ path: path, method: method, op: op });
      });
    });
    Object.keys(byTag).forEach(function (t) {
      byTag[t].sort(function (a, b) { return (a.op['x-order'] || 0) - (b.op['x-order'] || 0); });
    });
    (SPEC.tags || []).forEach(function (t) {
      var items = byTag[t.name] || [];
      if (!items.length) return;
      var g = { name: t.name, nav: el('details'), sec: el('section', 'group'), entries: [], open: !narrow };
      g.nav.open = g.open;
      var sum = el('summary', null, t.name); sum.appendChild(el('span', 'n', String(items.length)));
      g.nav.appendChild(sum);
      g.sec.id = 'tag-' + slug(t.name);
      var h = el('h2', null, t.name); h.appendChild(el('span', 'n', items.length + (items.length === 1 ? ' route' : ' routes')));
      g.sec.appendChild(h);
      items.forEach(function (it) {
        var a = el('a'); a.href = '#op-' + it.op.operationId;
        a.appendChild(el('span', 'method ' + it.method, it.method.toUpperCase())); a.appendChild(el('span', null, short(it.path)));
        a.addEventListener('click', function () { setTimeout(openFromHash, 0); });
        g.nav.appendChild(a);
        var d = renderOp(it.path, it.method, it.op);
        g.sec.appendChild(d);
        d.addEventListener('toggle', function () { if (d.open && location.hash !== '#' + d.id) { try { history.replaceState(null, '', '#' + d.id); } catch (e) { /* ignore */ } markCurrent(); } });
        var params = (it.op.parameters || []).map(function (p) { return p.name; }).join(' ');
        var e = { a: a, d: d, g: g, id: d.id, text: [it.method, it.path, it.op.summary, it.op.operationId, t.name, params].join(' ').toLowerCase() };
        g.entries.push(e); ENTRIES.push(e);
      });
      nav.appendChild(g.nav); ops.appendChild(g.sec); GROUPS.push(g);
    });
    nav.appendChild(el('p', 'none', 'No route matches.')).hidden = true;
    renderSchemas();
    $('filter').addEventListener('input', applyFilter);
    $('filter').addEventListener('keydown', function (e) {
      if (e.key === 'Escape') { this.value = ''; applyFilter(); }
      else if (e.key === 'Enter') { var v = ENTRIES.filter(function (x) { return !x.a.hidden; }); if (v.length) { location.hash = v[0].id; } }
    });
    applyFilter();
    openFromHash();
  }

  // The filter narrows the list on the left and the routes on the right by every word typed, in the path, summary, group or parameter names.
  function applyFilter() {
    var terms = $('filter').value.toLowerCase().split(/\s+/).filter(Boolean), shown = 0;
    ENTRIES.forEach(function (x) {
      var hit = terms.every(function (t) { return x.text.indexOf(t) >= 0; });
      x.a.hidden = x.d.hidden = !hit; if (hit) shown++;
    });
    GROUPS.forEach(function (g) {
      var n = g.entries.filter(function (x) { return !x.a.hidden; }).length;
      g.nav.hidden = g.sec.hidden = n === 0;
      g.nav.firstChild.lastChild.textContent = terms.length ? n + '/' + g.entries.length : String(g.entries.length);
      if (terms.length) g.nav.open = n > 0; else g.nav.open = g.open;
    });
    $('nav').lastChild.hidden = shown > 0;
    $('count').textContent = terms.length ? shown + ' of ' + ENTRIES.length + ' routes' : '';
  }

  function markCurrent() {
    ENTRIES.forEach(function (x) { if (location.hash === '#' + x.id) x.a.setAttribute('aria-current', 'true'); else x.a.removeAttribute('aria-current'); });
  }

  // A link to a route (#op-getTraces), a group (#tag-traces) or an object (#schema-Trace) opens it and shows it, whatever the filter says.
  function openFromHash() {
    markCurrent();
    var id = location.hash.slice(1), d = id ? document.getElementById(id) : null;
    if (!d) return;
    if (d.hidden) { $('filter').value = ''; applyFilter(); }
    if (d.tagName === 'DETAILS') d.open = true;
    d.scrollIntoView();
  }
  window.addEventListener('hashchange', openFromHash);

  // "/" jumps to the filter, as on most documentation sites, unless a field already has the keys.
  document.addEventListener('keydown', function (e) {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
    var t = e.target, tag = t && t.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (t && t.isContentEditable)) return;
    e.preventDefault(); $('filter').focus();
  });

  $('token').value = TOKEN;
  $('auth').onsubmit = function (e) {
    e.preventDefault(); TOKEN = $('token').value.trim();
    try { TOKEN ? sessionStorage.setItem('fusion-docs-token', TOKEN) : sessionStorage.removeItem('fusion-docs-token'); } catch (x) { /* ignore */ }
    checkWho();
  };
  fetch('openapi.json', { headers: { Accept: 'application/json' } }).then(function (r) { return r.json(); }).then(function (j) {
    SPEC = j; render(); if (TOKEN) checkWho();
  }).catch(function () { $('intro').appendChild(el('p', 'err', 'Could not load the API description.')); });
})();
