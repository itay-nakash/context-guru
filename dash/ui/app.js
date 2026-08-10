// context-guru dashboard. No framework, no build step, no CDN — the page is three
// files in a Go binary. State is a plain object, rendering is direct DOM writes,
// and charts are hand-drawn SVG. Everything that reads provider- or agent-supplied
// text goes through textContent or el(), never innerHTML, because a tool output in
// a transcript is attacker-influenced content (gateway interpolates it; we do not).
'use strict';

// ── tiny DOM helpers ───────────────────────────────────────────────────────
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));

/** el(tag, props, ...children) — children are appended as text unless they are Nodes. */
function el(tag, props, ...kids) {
  const n = document.createElement(tag);
  if (props) for (const [k, v] of Object.entries(props)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'class') n.className = v;
    else if (k === 'style') setStyle(n, v);
    else if (k === 'text') n.textContent = String(v);
    else if (k === 'html') throw new Error('el(): raw html is not allowed');
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, String(v));
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    n.appendChild(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return n;
}
const clear = (n) => { while (n.firstChild) n.removeChild(n.firstChild); return n; };

/**
 * setStyle applies "prop:value;prop:value" via the CSSOM.
 *
 * Not as a style ATTRIBUTE: the page ships a strict `style-src 'self'` CSP, which
 * blocks inline style attributes — and that CSP is worth keeping, because the diff
 * view renders tool output the model was fed, i.e. attacker-influenced text. Going
 * through el.style.setProperty is exempt from style-src and equally expressive.
 */
function setStyle(node, decls) {
  for (const part of String(decls).split(';')) {
    const i = part.indexOf(':');
    if (i < 0) continue;
    const prop = part.slice(0, i).trim();
    const val = part.slice(i + 1).trim();
    if (prop) node.style.setProperty(prop, val);
  }
}
const svgEl = (tag, attrs) => {
  const n = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [k, v] of Object.entries(attrs || {})) n.setAttribute(k, String(v));
  return n;
};

// ── formatting ─────────────────────────────────────────────────────────────
const nf = new Intl.NumberFormat();
function num(v) { return v === null || v === undefined ? '—' : nf.format(Math.round(v)); }
function compact(v) {
  if (v === null || v === undefined) return '—';
  const a = Math.abs(v);
  if (a >= 1e9) return (v / 1e9).toFixed(a >= 1e10 ? 0 : 1) + 'B';
  if (a >= 1e6) return (v / 1e6).toFixed(a >= 1e7 ? 0 : 1) + 'M';
  if (a >= 1e3) return (v / 1e3).toFixed(a >= 1e4 ? 0 : 1) + 'k';
  return nf.format(Math.round(v));
}
function usd(v) {
  if (v === null || v === undefined) return '—';
  const a = Math.abs(v);
  if (a === 0) return '$0';
  if (a < 0.01) return (v < 0 ? '-' : '') + '$' + a.toFixed(4);
  if (a < 1000) return (v < 0 ? '-' : '') + '$' + a.toFixed(2);
  return (v < 0 ? '-' : '') + '$' + nf.format(Math.round(a));
}
function pct(v, digits = 1) { return v === null || v === undefined ? '—' : v.toFixed(digits) + '%'; }
function ms(v) {
  if (!v) return '0 ms';
  return v >= 1000 ? (v / 1000).toFixed(2) + ' s' : v.toFixed(v < 10 ? 1 : 0) + ' ms';
}
// Timestamps are epoch ms on the wire and formatted here, in the VIEWER's locale.
// The server never stores or sends a formatted date — a locale string cannot be
// range-queried, sorted, or bucketed.
function when(tsMs) {
  if (!tsMs) return '—';
  const d = new Date(tsMs);
  const today = new Date();
  const sameDay = d.toDateString() === today.toDateString();
  return sameDay ? d.toLocaleTimeString() : d.toLocaleString();
}
function dur(msv) {
  if (!msv || msv < 0) return '—';
  // Below a second, show the actual milliseconds: rounding a component's 300 ms of
  // total hot-path time to "0s" hides exactly the cost this view exists to expose.
  if (msv < 1000) return ms(msv);
  const s = Math.round(msv / 1000);
  if (s < 60) return s + 's';
  const m = Math.floor(s / 60);
  if (m < 60) return m + 'm ' + (s % 60) + 's';
  return Math.floor(m / 60) + 'h ' + (m % 60) + 'm';
}
function firstOf(csv) { return (csv || '').split(',')[0] || '—'; }

// ── state ──────────────────────────────────────────────────────────────────
const state = {
  view: 'overview',
  filter: {},
  range: 0,
  reqCursor: 0,
  reqStack: [],
  sessOffset: 0,
  live: [],
  overview: null,
};

function qs(extra) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(state.filter)) if (v) p.set(k, v);
  if (state.range > 0) p.set('since', String(Date.now() - state.range));
  for (const [k, v] of Object.entries(extra || {})) if (v !== '' && v !== 0 && v !== undefined) p.set(k, String(v));
  const s = p.toString();
  return s ? '?' + s : '';
}

async function api(path, extra) {
  const res = await fetch('/api/' + path + qs(extra), { headers: { accept: 'application/json' } });
  if (!res.ok) {
    let msg = res.status + ' ' + res.statusText;
    try { const j = await res.json(); if (j.error) msg = j.error; } catch (_) { /* not json */ }
    const e = new Error(msg); e.status = res.status; throw e;
  }
  return res.json();
}

function emptyState(host, title, detail) {
  clear(host).appendChild(el('div', { class: 'empty' }, el('strong', { text: title }), detail || ''));
}
function loadingState(host, rows = 3) {
  clear(host);
  for (let i = 0; i < rows; i++) {
    host.appendChild(el('div', { class: 'skel', style: 'margin:8px 0;width:' + (100 - i * 12) + '%' }));
  }
}

// ── charts ─────────────────────────────────────────────────────────────────
// A tiny SVG line/area/bar renderer. Native SVG covers everything the issue asks
// for; a vendored chart library would add 45 KB to buy the tooltip below.
const CH = { w: 900, h: 220, pad: { t: 12, r: 14, b: 26, l: 56 } };

function ticks(min, max, n = 4) {
  if (!isFinite(min) || !isFinite(max) || max === min) return [min || 0];
  const step = (max - min) / n, out = [];
  for (let i = 0; i <= n; i++) out.push(min + step * i);
  return out;
}

/**
 * lineChart(host, series, opts)
 * series: [{name, color, points:[[x,y],…], area?:bool, dashed?:bool}]
 * opts: {yFmt, xFmt, tipFmt, stacked?}
 */
function lineChart(host, series, opts = {}) {
  clear(host);
  const live = series.filter((s) => s.points && s.points.length);
  if (!live.length) { emptyState(host, 'No data in this window', 'Send traffic through the proxy, or widen the time range.'); return; }
  const yFmt = opts.yFmt || compact, xFmt = opts.xFmt || when;

  const xs = live.flatMap((s) => s.points.map((p) => p[0]));
  const ys = live.flatMap((s) => s.points.map((p) => p[1]));
  const xMin = Math.min(...xs), xMax = Math.max(...xs);
  const yMin = Math.min(0, ...ys), yMax = Math.max(...ys) || 1;
  const { w, h, pad } = CH;
  const px = (x) => pad.l + (xMax === xMin ? 0 : ((x - xMin) / (xMax - xMin)) * (w - pad.l - pad.r));
  const py = (y) => h - pad.b - ((y - yMin) / (yMax - yMin || 1)) * (h - pad.t - pad.b);

  const svg = svgEl('svg', { viewBox: `0 0 ${w} ${h}`, role: 'img', preserveAspectRatio: 'none' });
  svg.setAttribute('aria-label', opts.label || 'time series chart');

  for (const t of ticks(yMin, yMax)) {
    svg.appendChild(svgEl('line', { class: 'gridline', x1: pad.l, x2: w - pad.r, y1: py(t), y2: py(t) }));
    const lab = svgEl('text', { class: 'axis-text', x: pad.l - 6, y: py(t) + 3, 'text-anchor': 'end' });
    lab.textContent = yFmt(t);
    svg.appendChild(lab);
  }
  svg.appendChild(svgEl('line', { class: 'axis', x1: pad.l, x2: w - pad.r, y1: h - pad.b, y2: h - pad.b }));
  for (const t of [xMin, (xMin + xMax) / 2, xMax]) {
    const lab = svgEl('text', {
      class: 'axis-text', x: px(t), y: h - pad.b + 14,
      'text-anchor': t === xMin ? 'start' : t === xMax ? 'end' : 'middle',
    });
    lab.textContent = xFmt(t);
    svg.appendChild(lab);
  }

  // Shaded band between the first two series (the "money saved" area).
  if (opts.band && live.length >= 2) {
    const a = live[0].points, b = live[1].points;
    const dPath = a.map((p, i) => `${i ? 'L' : 'M'}${px(p[0])},${py(p[1])}`).join('') +
      b.slice().reverse().map((p) => `L${px(p[0])},${py(p[1])}`).join('') + 'Z';
    svg.appendChild(svgEl('path', { d: dPath, fill: live[0].color, opacity: '0.14' }));
  }

  for (const s of live) {
    const d = s.points.map((p, i) => `${i ? 'L' : 'M'}${px(p[0])},${py(p[1])}`).join('');
    if (s.area) {
      svg.appendChild(svgEl('path', {
        d: d + `L${px(s.points[s.points.length - 1][0])},${py(yMin)}L${px(s.points[0][0])},${py(yMin)}Z`,
        fill: s.color, opacity: '0.13',
      }));
    }
    svg.appendChild(svgEl('path', {
      d, fill: 'none', stroke: s.color, 'stroke-width': 2,
      'stroke-linejoin': 'round', 'stroke-linecap': 'round',
      'stroke-dasharray': s.dashed ? '5 4' : null,
    }));
    // A path through a single point renders nothing, so one bucket of traffic would
    // look identical to no traffic. Draw explicit markers on short series.
    if (s.points.length <= 12) {
      for (const pt of s.points) {
        svg.appendChild(svgEl('circle', { cx: px(pt[0]), cy: py(pt[1]), r: 3.5, fill: s.color }));
      }
    }
  }

  const hover = svgEl('line', { class: 'axis', x1: 0, x2: 0, y1: pad.t, y2: h - pad.b, opacity: '0' });
  svg.appendChild(hover);
  // A transparent capture rect over the plot area. Without it, pointer events only
  // land on the rendered strokes — an SVG's own box is not a hit target — so the
  // tooltip fires on a 2px line and nowhere else. Added last so it sits on top.
  svg.appendChild(svgEl('rect', {
    x: pad.l, y: pad.t, width: Math.max(0, w - pad.l - pad.r), height: Math.max(0, h - pad.t - pad.b),
    fill: 'transparent',
  }));
  host.appendChild(svg);

  const tip = el('div', { class: 'tooltip' });
  host.appendChild(tip);
  svg.addEventListener('pointerleave', () => { tip.classList.remove('show'); hover.setAttribute('opacity', '0'); });
  svg.addEventListener('pointermove', (ev) => {
    const rect = svg.getBoundingClientRect();
    const relX = ((ev.clientX - rect.left) / rect.width) * w;
    const dataX = xMin + ((relX - pad.l) / (w - pad.l - pad.r)) * (xMax - xMin);
    let best = null;
    for (const p of live[0].points) if (!best || Math.abs(p[0] - dataX) < Math.abs(best[0] - dataX)) best = p;
    if (!best) return;
    hover.setAttribute('x1', px(best[0])); hover.setAttribute('x2', px(best[0]));
    hover.setAttribute('opacity', '0.5');
    const lines = [xFmt(best[0])];
    for (const s of live) {
      const p = s.points.find((q) => q[0] === best[0]);
      if (p) lines.push(s.name + ': ' + (opts.tipFmt || yFmt)(p[1]));
    }
    tip.textContent = lines.join('\n');
    tip.classList.add('show');
    const hostRect = host.getBoundingClientRect();
    tip.style.left = Math.min(hostRect.width - 190, Math.max(0, ev.clientX - hostRect.left + 12)) + 'px';
    tip.style.top = Math.max(0, ev.clientY - hostRect.top - 10) + 'px';
  });

  host.appendChild(el('div', { class: 'legend' }, ...live.map((s) =>
    el('span', {}, el('i', { style: 'background:' + s.color }), s.name))));
}

/** barRows(host, rows) — rows: [{label, value, display, max, negative, desc}] */
function barRows(host, rows, opts = {}) {
  clear(host);
  if (!rows.length) { emptyState(host, 'Nothing to show yet', opts.emptyDetail || ''); return; }
  const max = Math.max(...rows.map((r) => Math.abs(r.max !== undefined ? r.max : r.value)), 1);
  const wrap = el('div', { class: 'bars' });
  for (const r of rows) {
    const width = r.available === false ? 0 : Math.min(100, (Math.abs(r.value) / max) * 100);
    const row = el('div', { class: 'bar-row' },
      el('div', { class: 'bar-label', text: r.label }),
      el('div', { class: 'bar-track' }, el('div', {
        class: 'bar-fill' + (r.value < 0 ? ' neg' : ''),
        style: 'width:' + width + '%' + (r.color ? ';background:' + r.color : ''),
      })),
      el('div', { class: 'bar-val' + (r.available === false ? ' na' : ''), text: r.display }));
    wrap.appendChild(row);
    if (r.desc) wrap.appendChild(el('div', { class: 'bar-desc', text: r.desc }));
  }
  host.appendChild(wrap);
}

// ── overview ───────────────────────────────────────────────────────────────
function tile(key, label, value, sub, cls) {
  return el('div', { class: 'tile ' + (cls || ''), 'data-testid': 'tile-' + key },
    el('div', { class: 'k', text: label }),
    el('div', { class: 'v', 'data-testid': 'tile-' + key + '-value', text: value }),
    sub ? el('div', { class: 's', text: sub }) : null);
}

function renderTiles(o) {
  const host = clear($('#tiles'));
  const exact = (o.accounting && o.accounting.complete) || 0;
  const costKnown = exact > 0;
  const tiles = [
    tile('requests', 'Requests', num(o.requests), num(o.sessions) + ' sessions'),
    tile('tokens-before', 'Tokens before', compact(o.tokens_before), 'content tokens in'),
    tile('tokens-after', 'Tokens after', compact(o.tokens_after), 'content tokens out'),
    tile('saved-gross', 'Saved (gross)', compact(o.saved_gross), 'recounts re-sent history', 'accent'),
    tile('saved-unique', 'Saved (unique)', compact(o.saved_unique), 'each compaction once', 'good'),
    tile('saved-adjusted', 'Saved (net of restores)', compact(o.saved_adjusted),
      compact(o.expand_tokens) + ' restored back', o.saved_adjusted < 0 ? 'bad' : ''),
    tile('overcount', 'Overcount ratio', o.overcount_ratio ? o.overcount_ratio.toFixed(1) + '×' : '—',
      'gross ÷ unique'),
    tile('cost-baseline', 'Baseline cost', costKnown ? usd(o.baseline_cost_usd) : 'unknown',
      costKnown ? 'without context-guru' : 'no priced requests'),
    tile('cost-actual', 'Actual cost', costKnown ? usd(o.cost_usd) : 'unknown',
      costKnown ? 'as billed' : 'no priced requests'),
    tile('cost-cg', "context-guru's own LLM", costKnown ? usd(o.cg_llm_cost_usd) : 'unknown',
      'our components’ model spend'),
    tile('saved-usd', 'Net dollars saved', costKnown ? usd(o.net_saved_usd) : 'unknown',
      'baseline − actual − our spend', o.net_saved_usd < 0 ? 'bad' : 'good'),
    tile('cache-read', 'Cache reads', compact(o.cache_read), 'billed at the read rate'),
    tile('cache-write', 'Cache writes', compact(o.cache_write), '~11.5× a read'),
    tile('fresh-input', 'Fresh input', compact(o.fresh_input), 'uncached new tokens'),
    tile('output', 'Output tokens', compact(o.output_tokens), 'completions'),
    tile('cg-latency', 'context-guru latency', ms(o.cg_latency_ms_avg), 'p95 ' + ms(o.cg_latency_ms_p95)),
    tile('upstream-latency', 'Upstream latency', ms(o.upstream_ms_avg), 'p95 ' + ms(o.upstream_ms_p95)),
    tile('expands', 'Restorations', num(o.expands),
      pct(o.expand_rate * 100) + ' of requests · ' + compact(o.expand_tokens) + ' tok',
      o.expands > 0 ? 'bad' : ''),
    tile('reverts', 'Reverts', num(o.reverts), 'never-worse guard fired'),
    tile('passthroughs', 'Not compacted', num(o.passthroughs), 'see reason buckets below'),
  ];
  tiles.forEach((t) => host.appendChild(t));
}

function renderDenominators(o) {
  barRows($('#denominators'), (o.denominators || []).map((d) => ({
    label: d.label,
    value: d.available ? d.percent : 0,
    max: 100,
    display: d.available ? pct(d.percent, 2) : 'n/a',
    available: d.available,
    desc: d.description + (d.available ? `  (${compact(d.numerator)} ÷ ${compact(d.denominator)} tokens)` : ''),
  })), { emptyDetail: 'No requests match the filter.' });
}

function renderWaterfall(o) {
  const host = clear($('#waterfall'));
  const steps = o.waterfall || [];
  if (!steps.length || !o.baseline_cost_usd) {
    emptyState(host, 'No priced requests yet',
      'The waterfall needs provider usage data (all four token tiers) and a known model price.');
    return;
  }
  const max = Math.max(...steps.map((s) => Math.abs(s.delta_usd)), 0.0001);
  const wrap = el('div', { class: 'bars' });
  for (const s of steps) {
    const color = s.total ? 'var(--s2)' : s.delta_usd < 0 ? 'var(--good)' : 'var(--bad)';
    wrap.appendChild(el('div', { class: 'bar-row' },
      el('div', { class: 'bar-label', text: s.label }),
      el('div', { class: 'bar-track' }, el('div', {
        class: 'bar-fill', style: `width:${(Math.abs(s.delta_usd) / max) * 100}%;background:${color}`,
      })),
      el('div', { class: 'bar-val', text: (s.delta_usd < 0 ? '−' : s.total ? '' : '+') + usd(Math.abs(s.delta_usd)) })));
    wrap.appendChild(el('div', { class: 'bar-desc', text: s.description }));
  }
  host.appendChild(wrap);
}

function renderDistribution(hostSel, map, labels, testid) {
  const host = clear($(hostSel));
  const entries = Object.entries(map || {}).filter(([, v]) => v > 0);
  if (!entries.length) { emptyState(host, 'No requests in this window', ''); return; }
  entries.sort((a, b) => b[1] - a[1]);
  const total = entries.reduce((n, [, v]) => n + v, 0);
  barRows(host, entries.map(([k, v]) => ({
    label: (labels && labels[k]) || (k === '' ? 'compacted' : k),
    value: v, max: total,
    display: num(v) + '  (' + pct((v / total) * 100, 0) + ')',
  })));
}

function renderSafety(o) {
  const s = o.safety_cost || {};
  $('#safety-note').textContent = s.description || '';
  barRows($('#safety'), [
    { label: 'Frozen for cache safety', value: s.frozen_tokens || 0, display: compact(s.frozen_tokens) + ' tok',
      desc: 'Compaction we deliberately did NOT do on the already-cached prefix. The benefit ' +
            'is the ' + compact(o.cache_read) + ' cache-read tokens that stayed cheap; the cost is this.' },
    { label: 'Restored after offload', value: s.restored_tokens || 0, display: compact(s.restored_tokens) + ' tok',
      color: 'var(--bad)',
      desc: 'Content we removed and the model asked back for — a premature offload, paid for twice.' },
    { label: 'Reverted component runs', value: s.reverted_runs || 0, display: num(s.reverted_runs) + ' runs',
      color: 'var(--s3)',
      desc: 'The never-worse guard rolling a component back. Safety working, and its cost is the ' +
            'latency of the attempt.' },
    { label: "context-guru's own latency", value: s.cg_latency_ms_total || 0, display: dur(s.cg_latency_ms_total),
      color: 'var(--s4)', desc: 'Total wall time context-guru itself added across the window.' },
    { label: "context-guru's own LLM spend", value: (s.cg_llm_cost_usd || 0) * 1000, display: usd(s.cg_llm_cost_usd),
      color: 'var(--s5)', desc: 'Paid out of the savings above.' },
  ]);
}

function renderLive() {
  const body = clear($('#live-body'));
  if (!state.live.length) {
    body.appendChild(el('tr', {}, el('td', { colspan: '8' },
      el('div', { class: 'empty' }, el('strong', { text: 'Waiting for traffic' }),
        'Requests appear here the moment they are captured.'))));
    return;
  }
  for (const e of state.live.slice(0, 25)) {
    body.appendChild(el('tr', { class: 'click', onclick: () => openRequest(e.id) },
      el('td', { text: when(e.ts) }),
      el('td', {}, el('span', { class: 'trunc', title: e.session_id, text: e.session_id || '—' })),
      el('td', { text: e.model || '—' }),
      el('td', { class: 'num', text: compact(e.tokens_before) }),
      el('td', { class: 'num', text: compact(e.tokens_after) }),
      el('td', { class: 'num', text: compact(e.tokens_before - e.tokens_after) }),
      el('td', { class: 'num', text: ms(e.cg_latency_ms) }),
      el('td', {}, el('span', { class: 'pill ' + e.token_accounting, text: e.token_accounting }))));
  }
}

async function loadOverview() {
  loadingState($('#tiles'), 4);
  try {
    const [o, s] = await Promise.all([api('stats'), api('series', { bucket: bucketFor() })]);
    state.overview = o;
    renderTiles(o);
    renderDenominators(o);
    renderWaterfall(o);
    renderSafety(o);
    renderDistribution('#cachemiss', o.cache_miss, {
      hit: 'cache hit', cold_start: 'cold start (not a failure)', ttl_expiry: 'TTL expiry',
      prefix_change: 'prefix change', unknown: 'unknown', '': 'no cache data',
    });
    renderDistribution('#reasons', o.uncompressed, {
      '': 'compacted', bypassed: 'bypassed by header', below_trigger: 'below every trigger',
      cache_frozen: 'frozen for cache safety', found_nothing: 'nothing to remove',
      reverted: 'all components reverted', no_messages: 'no messages',
    });
    renderDistribution('#accounting', o.accounting, {
      complete: 'exact (all four tiers)', partial: 'estimated', missing: 'unmeasured',
    });
    renderSeries(s.buckets || []);
  } catch (err) {
    emptyState($('#tiles'), 'Could not load statistics', String(err.message || err));
  }
}

function bucketFor() {
  if (state.range === 0) return 3600000;
  if (state.range <= 3600000) return 60000;
  if (state.range <= 86400000) return 300000;
  return 3600000;
}

function renderSeries(buckets) {
  if (!buckets.length) {
    for (const id of ['#chart-cost', '#chart-tokens', '#chart-cache', '#chart-latency', '#chart-volume']) {
      emptyState($(id), 'No data in this window', 'Send traffic through the proxy, or widen the time range.');
    }
    return;
  }
  // Cumulative cost: the headline chart. The area between the lines is the money.
  let cumBase = 0, cumAct = 0;
  const base = [], act = [];
  for (const b of buckets) {
    cumBase += b.baseline_cost_usd;
    cumAct += b.cost_usd + b.cg_llm_cost_usd;
    base.push([b.ts, cumBase]);
    act.push([b.ts, cumAct]);
  }
  const anyCost = cumBase > 0 || cumAct > 0;
  if (anyCost) {
    lineChart($('#chart-cost'), [
      { name: 'Without context-guru (cumulative)', color: 'var(--s5)', points: base },
      { name: 'With context-guru (incl. our own spend)', color: 'var(--s1)', points: act, area: true },
    ], { band: true, yFmt: usd, tipFmt: usd, label: 'cumulative cost with and without context-guru' });
  } else {
    emptyState($('#chart-cost'), 'No priced requests yet',
      'Cost needs provider usage data (all four token tiers) and a known model price. Token charts below still work.');
  }

  lineChart($('#chart-tokens'), [
    { name: 'Tokens before', color: 'var(--s2)', points: buckets.map((b) => [b.ts, b.tokens_before]) },
    { name: 'Tokens after', color: 'var(--s1)', points: buckets.map((b) => [b.ts, b.tokens_after]), area: true },
    { name: 'Saved (unique)', color: 'var(--s3)', points: buckets.map((b) => [b.ts, b.saved_unique]) },
  ], { label: 'content tokens over time' });

  lineChart($('#chart-cache'), [
    { name: 'Cache reads', color: 'var(--s1)', points: buckets.map((b) => [b.ts, b.cache_read]), area: true },
    { name: 'Cache writes', color: 'var(--s3)', points: buckets.map((b) => [b.ts, b.cache_write]) },
    { name: 'Fresh input', color: 'var(--s2)', points: buckets.map((b) => [b.ts, b.fresh_input]) },
  ], { label: 'cache reads versus writes over time' });

  lineChart($('#chart-latency'), [
    { name: 'context-guru added (avg)', color: 'var(--s1)', points: buckets.map((b) => [b.ts, b.cg_latency_ms_avg]) },
    { name: 'Upstream round-trip (avg)', color: 'var(--s2)', points: buckets.map((b) => [b.ts, b.upstream_ms_avg]), dashed: true },
  ], { yFmt: ms, tipFmt: ms, label: 'latency over time' });

  lineChart($('#chart-volume'), [
    { name: 'Requests', color: 'var(--s2)', points: buckets.map((b) => [b.ts, b.requests]), area: true },
    { name: 'Restorations (expands)', color: 'var(--s5)', points: buckets.map((b) => [b.ts, b.expands]) },
    { name: 'Cache misses', color: 'var(--s3)', points: buckets.map((b) => [b.ts, b.cache_misses]) },
  ], { yFmt: num, label: 'request volume and restorations' });
}

// ── components ─────────────────────────────────────────────────────────────
/**
 * verdict summarises whether a component earns its place, from what it saved
 * against what it cost. Order matters: a component that burned real wall time for
 * nothing is a worse finding than one that simply never fired, so the cost test
 * comes FIRST — otherwise extract_llm's 15 s of model calls for zero savings reads
 * as a bland "inert here".
 */
function verdict(c) {
  if (c.runs === 0) return ['—', 'neutral'];
  if (c.errors > 0) return ['errors', 'missing'];
  // Spent >1s of hot-path time and returned nothing: paid for, unused.
  if (c.saved_unique === 0 && c.duration_ms_total > 1000) return ['costly and inert', 'missing'];
  if (c.mutated === 0) return ['inert here', 'partial'];
  if (c.saved_unique === 0) return ['mutates, saves no content', 'neutral'];
  // More than a millisecond of latency per 100 tokens saved.
  if (c.duration_ms_total > 1000 && c.duration_ms_total / c.saved_unique > 0.01) {
    return ['expensive for its yield', 'partial'];
  }
  if (c.act_rate < 0.02) return ['rarely fires', 'partial'];
  return ['earning its place', 'complete'];
}

async function loadComponents() {
  const body = clear($('#components-body'));
  body.appendChild(el('tr', {}, el('td', { colspan: '13' }, el('div', { class: 'skel' }))));
  try {
    const { components } = await api('components');
    clear(body);
    if (!components.length) {
      body.appendChild(el('tr', {}, el('td', { colspan: '13' },
        el('div', { class: 'empty' }, el('strong', { text: 'No component runs captured' }),
          'Run some traffic through the proxy with a non-empty pipeline.'))));
      emptyState($('#chart-comp'), 'No component data', '');
      return;
    }
    for (const c of components) {
      const [vtext, vcls] = verdict(c);
      body.appendChild(el('tr', { class: 'click', onclick: () => { setFilter('component', c.component); go('requests'); } },
        el('td', {}, el('code', { text: c.component })),
        el('td', { text: c.kind || '—' }),
        el('td', { class: 'num', text: num(c.runs) }),
        el('td', { class: 'num', text: num(c.acted) }),
        el('td', { class: 'num', text: pct(c.act_rate * 100, 1) }),
        el('td', { class: 'num', text: num(c.reverted) }),
        el('td', { class: 'num', text: compact(c.saved_unique) }),
        el('td', { class: 'num', text: compact(c.saved_gross) }),
        el('td', { class: 'num', text: c.overcount_ratio ? c.overcount_ratio.toFixed(1) + '×' : '—' }),
        el('td', { class: 'num', text: dur(c.duration_ms_total) }),
        el('td', { class: 'num', text: ms(c.duration_ms_avg) }),
        el('td', { class: 'num', text: num(c.errors) }),
        el('td', {}, el('span', { class: 'pill ' + vcls, text: vtext }))));
    }
    const top = components.filter((c) => c.saved_unique > 0).slice(0, 12);
    barRows($('#chart-comp'), top.map((c, i) => ({
      label: c.component, value: c.saved_unique, display: compact(c.saved_unique) + ' tok',
      color: `var(--s${(i % 5) + 1})`,
      desc: `${num(c.runs)} runs, acted on ${pct(c.act_rate * 100, 1)}, own latency ${dur(c.duration_ms_total)}, ` +
            `overcount ${c.overcount_ratio ? c.overcount_ratio.toFixed(1) + '×' : 'n/a'}`,
    })), { emptyDetail: 'No component saved any content tokens in this window.' });
  } catch (err) {
    clear(body).appendChild(el('tr', {}, el('td', { colspan: '13' },
      el('div', { class: 'empty' }, el('strong', { text: 'Could not load components' }), String(err.message || err)))));
  }
}

// ── sessions ───────────────────────────────────────────────────────────────
async function loadSessions() {
  const body = clear($('#sessions-body'));
  body.appendChild(el('tr', {}, el('td', { colspan: '12' }, el('div', { class: 'skel' }))));
  try {
    const { sessions, total } = await api('sessions', { limit: 25, offset: state.sessOffset });
    clear(body);
    if (!sessions.length) {
      body.appendChild(el('tr', {}, el('td', { colspan: '12' },
        el('div', { class: 'empty' }, el('strong', { text: 'No sessions yet' }),
          'A session appears as soon as its first request is captured.'))));
    }
    for (const s of sessions) {
      body.appendChild(el('tr', { class: 'click', onclick: () => { setFilter('session', s.session_id); go('requests'); } },
        el('td', {}, el('span', { class: 'trunc', title: s.session_id, text: s.session_id || '(none)' })),
        el('td', { text: firstOf(s.models) }),
        el('td', { text: firstOf(s.agents) }),
        el('td', { text: firstOf(s.presets) }),
        el('td', { class: 'num', text: num(s.turns) }),
        el('td', { class: 'num', text: compact(s.tokens_before) }),
        el('td', { class: 'num', text: compact(s.saved) }),
        el('td', { class: 'num', text: s.baseline_cost_usd ? usd(s.saved_usd) : '—' }),
        el('td', { class: 'num', text: compact(s.cache_read) + ' / ' + compact(s.cache_write) }),
        el('td', { class: 'num', text: num(s.expands) }),
        el('td', { class: 'num', text: ms(s.cg_latency_ms_avg) }),
        el('td', { text: when(s.start) })));
    }
    const from = total ? state.sessOffset + 1 : 0;
    $('#sess-page').textContent = `${from}–${Math.min(state.sessOffset + 25, total)} of ${num(total)}`;
    $('#sess-prev').disabled = state.sessOffset === 0;
    $('#sess-next').disabled = state.sessOffset + 25 >= total;
  } catch (err) {
    clear(body).appendChild(el('tr', {}, el('td', { colspan: '12' },
      el('div', { class: 'empty' }, el('strong', { text: 'Could not load sessions' }), String(err.message || err)))));
  }
}

// ── requests ───────────────────────────────────────────────────────────────
async function loadRequests() {
  const body = clear($('#requests-body'));
  body.appendChild(el('tr', {}, el('td', { colspan: '13' }, el('div', { class: 'skel' }))));
  try {
    const page = await api('requests', { limit: 50, before: state.reqCursor });
    clear(body);
    if (!page.requests.length) {
      body.appendChild(el('tr', {}, el('td', { colspan: '13' },
        el('div', { class: 'empty' }, el('strong', { text: 'No requests match' }),
          'Clear a filter, widen the range, or send traffic through the proxy.'))));
    }
    for (const e of page.requests) {
      body.appendChild(el('tr', { class: 'click', 'data-testid': 'request-row', onclick: () => openRequest(e.id) },
        el('td', { text: e.id }),
        el('td', { text: when(e.ts) }),
        el('td', {}, el('span', { class: 'trunc', title: e.session_id, text: e.session_id || '—' })),
        el('td', { text: e.model || '—' }),
        el('td', {}, el('span', { class: 'pill neutral', text: e.mode || '—' })),
        el('td', { class: 'num', text: compact(e.tokens_before) }),
        el('td', { class: 'num', text: compact(e.tokens_after) }),
        el('td', { class: 'num', text: compact(e.tokens_before - e.tokens_after) }),
        el('td', { class: 'num', text: compact(e.cache_read) + ' / ' + compact(e.cache_write) }),
        el('td', { class: 'num', text: e.token_accounting === 'complete' ? usd(e.cost_usd) : '—' }),
        el('td', { class: 'num', text: ms(e.cg_latency_ms) }),
        el('td', {}, el('span', { class: 'pill ' + (e.cache_miss_reason || 'neutral'), text: e.cache_miss_reason || '—' })),
        el('td', {}, el('span', { class: 'pill ' + e.token_accounting, text: e.token_accounting }))));
    }
    $('#req-page').textContent = `${num(page.requests.length)} shown of ${num(page.total)} matching`;
    $('#req-next').disabled = !page.next_cursor;
    $('#req-prev').disabled = state.reqStack.length === 0;
    state.nextCursor = page.next_cursor;
  } catch (err) {
    clear(body).appendChild(el('tr', {}, el('td', { colspan: '13' },
      el('div', { class: 'empty' }, el('strong', { text: 'Could not load requests' }), String(err.message || err)))));
  }
}

// ── request detail + diff ──────────────────────────────────────────────────
/**
 * Myers-style LCS diff over lines, then rendered Git-style. This is the view both
 * reference implementations carry the data for and neither built: it answers
 * "what did context-guru actually remove or rewrite?" instead of asserting a
 * token count.
 */
function diffLines(a, b) {
  const n = a.length, m = b.length;
  // Trim the common head/tail first: agent transcripts share long identical
  // stretches, so this cuts the DP table to the part that actually differs.
  let head = 0;
  while (head < n && head < m && a[head] === b[head]) head++;
  let tail = 0;
  while (tail < n - head && tail < m - head && a[n - 1 - tail] === b[m - 1 - tail]) tail++;
  const as = a.slice(head, n - tail), bs = b.slice(head, m - tail);

  const out = [];
  for (let i = 0; i < head; i++) out.push({ op: ' ', text: a[i], ai: i + 1, bi: i + 1 });

  // Guard the quadratic table: a huge rewrite renders as a whole-block replace
  // rather than hanging the tab.
  // ponytail: LCS is O(n·m); the cap below is the ceiling. Switch to a real Myers
  // O(nd) if multi-megabyte single-message diffs ever matter.
  const LIMIT = 1500;
  if (as.length > LIMIT || bs.length > LIMIT) {
    if (as.length) out.push({ op: 'gap', text: `… ${as.length} lines replaced (too large to line-diff) …` });
    for (let i = 0; i < as.length; i++) out.push({ op: '-', text: as[i], ai: head + i + 1 });
    for (let j = 0; j < bs.length; j++) out.push({ op: '+', text: bs[j], bi: head + j + 1 });
  } else {
    const dp = Array.from({ length: as.length + 1 }, () => new Uint32Array(bs.length + 1));
    for (let i = as.length - 1; i >= 0; i--) {
      for (let j = bs.length - 1; j >= 0; j--) {
        dp[i][j] = as[i] === bs[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
      }
    }
    let i = 0, j = 0;
    while (i < as.length && j < bs.length) {
      if (as[i] === bs[j]) { out.push({ op: ' ', text: as[i], ai: head + i + 1, bi: head + j + 1 }); i++; j++; }
      else if (dp[i + 1][j] >= dp[i][j + 1]) { out.push({ op: '-', text: as[i], ai: head + i + 1 }); i++; }
      else { out.push({ op: '+', text: bs[j], bi: head + j + 1 }); j++; }
    }
    while (i < as.length) { out.push({ op: '-', text: as[i], ai: head + i + 1 }); i++; }
    while (j < bs.length) { out.push({ op: '+', text: bs[j], bi: head + j + 1 }); j++; }
  }
  for (let k = 0; k < tail; k++) {
    out.push({ op: ' ', text: a[n - tail + k], ai: n - tail + k + 1, bi: m - tail + k + 1 });
  }
  return out;
}

/** Collapse runs of unchanged lines to CTX lines of context, Git-style. */
function withHunks(rows, ctx = 3) {
  const keep = new Array(rows.length).fill(false);
  rows.forEach((r, i) => {
    if (r.op === ' ') return;
    for (let k = Math.max(0, i - ctx); k <= Math.min(rows.length - 1, i + ctx); k++) keep[k] = true;
  });
  const out = [];
  let skipped = 0;
  rows.forEach((r, i) => {
    if (keep[i]) {
      if (skipped) { out.push({ op: 'gap', text: `… ${skipped} unchanged lines …` }); skipped = 0; }
      out.push(r);
    } else skipped++;
  });
  if (skipped) out.push({ op: 'gap', text: `… ${skipped} unchanged lines …` });
  return out;
}

function renderDiff(host, before, after, mode) {
  clear(host);
  if (mode === 'side') {
    host.appendChild(el('div', { class: 'side' },
      el('pre', { text: before || '(empty)' }), el('pre', { text: after || '(empty)' })));
    return;
  }
  if (mode === 'raw') {
    host.appendChild(el('pre', { style: 'margin:0;padding:8px 10px;white-space:pre-wrap', text: after || '(empty)' }));
    return;
  }
  const rows = withHunks(diffLines((before || '').split('\n'), (after || '').split('\n')));
  if (!rows.length) { host.appendChild(el('div', { class: 'empty', text: 'Identical.' })); return; }
  const frag = document.createDocumentFragment();
  for (const r of rows) {
    if (r.op === 'gap') {
      frag.appendChild(el('div', { class: 'dl gap' },
        el('span', { class: 'ln' }), el('span', { class: 'ln' }), el('span', { class: 'tx', text: r.text })));
      continue;
    }
    const cls = r.op === '+' ? 'add' : r.op === '-' ? 'del' : 'ctx';
    frag.appendChild(el('div', { class: 'dl ' + cls },
      el('span', { class: 'ln', text: r.ai || '' }),
      el('span', { class: 'ln', text: r.bi || '' }),
      el('span', { class: 'tx', text: r.text })));
  }
  host.appendChild(frag);
}

function kv(k, v) { return el('div', {}, el('div', { class: 'k', text: k }), el('div', { class: 'v', text: v })); }

async function openRequest(id) {
  $('#drawer').hidden = false;
  $('#scrim').hidden = false;
  $('#drawer-title').textContent = 'Request #' + id;
  const body = clear($('#drawer-body'));
  loadingState(body, 5);
  try {
    const res = await fetch('/api/requests/' + id);
    if (!res.ok) throw new Error(res.status + ' ' + res.statusText);
    const { request: e, content_visible: visible, content_captured: captured } = await res.json();
    clear(body);

    body.appendChild(el('div', { class: 'kv', 'data-testid': 'detail-summary' },
      kv('Session', e.session_id || '—'),
      kv('When', when(e.ts)),
      kv('Model', e.model || '—'),
      kv('Provider', e.provider || '—'),
      kv('Agent', e.agent || '—'),
      kv('Preset', e.preset || '—'),
      kv('Mode', e.mode || '—'),
      kv('Upstream status', e.status || '—'),
      kv('Messages', num(e.messages)),
      kv('Tokens before → after', compact(e.tokens_before) + ' → ' + compact(e.tokens_after)),
      kv('Saved (gross / unique)', compact(e.tokens_before - e.tokens_after) + ' / ' + compact(e.saved_unique)),
      kv('Attempted (eligible)', compact(e.attempted_tokens)),
      kv('Frozen for cache safety', compact(e.frozen_tokens)),
      kv('Fresh / read / write / out',
        [e.fresh_input, e.cache_read, e.cache_write, e.output_tokens].map(compact).join(' / ')),
      kv('Cost (actual / baseline)', e.token_accounting === 'complete'
        ? usd(e.cost_usd) + ' / ' + usd(e.baseline_cost_usd) : 'not priced'),
      kv("context-guru's own LLM", e.token_accounting === 'complete' ? usd(e.cg_llm_cost_usd) : '—'),
      kv('context-guru latency', ms(e.cg_latency_ms)),
      kv('Upstream latency', ms(e.upstream_ms)),
      kv('Restorations', num(e.expands) + ' (' + compact(e.expand_tokens) + ' tok)'),
      kv('Reverts', num(e.reverts)),
      kv('Cache attribution', e.cache_miss_reason || '—'),
      kv('Token accounting', e.token_accounting),
      kv('Compaction outcome', e.uncompressed_reason || 'compacted')));

    body.appendChild(el('h2', { text: 'Components, in the order they ran' }));
    if (!e.components || !e.components.length) {
      body.appendChild(el('div', { class: 'empty', text: 'No components ran on this request.' }));
    } else {
      const tbl = el('table', { class: 'tbl compact', 'data-testid': 'detail-components' },
        el('thead', {}, el('tr', {},
          el('th', { text: '#' }), el('th', { text: 'Component' }), el('th', { text: 'Kind' }),
          el('th', { class: 'num', text: 'Saved' }), el('th', { class: 'num', text: 'Unique' }),
          el('th', { class: 'num', text: 'Latency' }), el('th', { text: 'Outcome' }))));
      const tb = el('tbody');
      e.components.forEach((c, i) => {
        const outcome = c.reverted ? ['reverted', 'missing'] : c.skipped ? ['skipped', 'neutral']
          : c.acted ? ['acted', 'complete'] : ['mutated only', 'partial'];
        tb.appendChild(el('tr', {},
          el('td', { text: i + 1 }),
          el('td', {}, el('code', { text: c.component })),
          el('td', { text: c.kind || '—' }),
          el('td', { class: 'num', text: compact(c.saved_gross) }),
          el('td', { class: 'num', text: compact(c.saved_unique) }),
          el('td', { class: 'num', text: ms(c.duration_ms) }),
          el('td', {}, el('span', { class: 'pill ' + outcome[1], text: outcome[0] }),
            c.err ? el('div', { class: 's', text: c.err }) : null)));
      });
      tbl.appendChild(tb);
      body.appendChild(el('div', { class: 'tblwrap' }, tbl));
    }

    body.appendChild(el('h2', { style: 'margin-top:18px', text: 'What context-guru changed' }));
    if (!visible) {
      body.appendChild(el('div', { class: 'empty' },
        el('strong', { text: 'Content is not visible from this address' }),
        'Per-request content is served to loopback or a configured trusted CIDR only, because a ' +
        'transcript can carry your source code. Aggregates are open.'));
    } else if (!captured) {
      body.appendChild(el('div', { class: 'empty' },
        el('strong', { text: 'Content capture is disabled' }),
        'Start the proxy with content capture on to record before/after text for the diff view.'));
    } else if (!e.content || !e.content.length) {
      body.appendChild(el('div', { class: 'empty' },
        el('strong', { text: 'Nothing was rewritten' }),
        'This request passed through unchanged' + (e.uncompressed_reason ? ' (' + e.uncompressed_reason + ')' : '') + '.'));
    } else {
      // Biggest saving first, and open that one: the point of the view is "what did
      // context-guru actually remove?", so leading with an unchanged block (and
      // collapsing the 2k-token rewrite below it) buries the answer.
      const blocks = e.content.slice().sort(
        (a, b) => (b.before_tokens - b.after_tokens) - (a.before_tokens - a.after_tokens));
      blocks.forEach((c, idx) => {
        const saved = c.before_tokens - c.after_tokens;
        const det = el('details', { class: 'diff', 'data-testid': 'diff-block' }, el('summary', {
          text: `${c.path} — ${compact(c.before_tokens)} → ${compact(c.after_tokens)} tokens ` +
                (saved > 0 ? `(saved ${compact(saved)})` : '(rewritten, no token saving)'),
        }));
        if (idx === 0 && c.before_tokens > c.after_tokens) det.open = true;
        const bodyHost = el('div', { class: 'diffbody' });
        const bar = el('div', { class: 'difftoolbar' }, 'View:');
        // testids spelled out in full so a grep (and the Go test that guards them)
        // finds them literally rather than reconstructing a concatenation.
        for (const [mode, label, testid] of [
          ['git', 'Git diff', 'diff-mode-git'],
          ['side', 'Side by side', 'diff-mode-side'],
          ['raw', 'After only', 'diff-mode-raw'],
        ]) {
          bar.appendChild(el('button', {
            class: 'ghost', 'data-testid': testid,
            onclick: () => renderDiff(bodyHost, c.before, c.after, mode),
          }, label));
        }
        det.appendChild(bar);
        det.appendChild(bodyHost);
        renderDiff(bodyHost, c.before, c.after, 'git');
        body.appendChild(det);
      });
    }
  } catch (err) {
    emptyState(clear(body), 'Could not load this request', String(err.message || err));
  }
}

function closeDrawer() { $('#drawer').hidden = true; $('#scrim').hidden = true; }

// ── benchmarks ─────────────────────────────────────────────────────────────
async function loadBenchmarks() {
  const host = clear($('#bench-list'));
  loadingState(host, 3);
  try {
    const { runs } = await api('benchmarks');
    clear(host);
    if (!runs || !runs.length) {
      emptyState(host, 'No benchmark runs ingested',
        'Point --dash-bench-dirs at a harness jobs root (a directory of runs, each with summary.json and rows-*.json) and re-scan.');
      return;
    }
    // 42 ingested runs rendered flat is 40k pixels of table. Collapse each run and
    // open only the newest, so the view opens on the run you just finished.
    runs.forEach((run, runIdx) => {
      const sec = el('details', { class: 'panel diff', 'data-testid': 'bench-run' });
      if (runIdx === 0) sec.open = true;
      const armNames = (run.arms || []).map((a) => a.arm).join(', ');
      sec.appendChild(el('summary', {},
        el('strong', { text: run.name }),
        '  ' + [run.dataset, run.model, armNames && 'arms: ' + armNames,
          when(run.ts)].filter(Boolean).join(' · ')));
      const inner = el('div', { style: 'padding:12px 14px' });
      const tbl = el('table', { class: 'tbl' }, el('thead', {}, el('tr', {},
        el('th', { text: 'Arm' }), el('th', { class: 'num', text: 'Tasks' }),
        el('th', { class: 'num', text: 'Solved' }), el('th', { class: 'num', text: 'Solve rate' }),
        el('th', { class: 'num', text: 'Mean reward' }), el('th', { class: 'num', text: 'Mean steps' }),
        el('th', { class: 'num', text: 'Total cost' }), el('th', { class: 'num', text: 'Cost / task' }),
        el('th', { class: 'num', text: '$ per solve' }),
        el('th', { class: 'num', text: 'Cache hit' }), el('th', { class: 'num', text: 'Mean wall' }),
        el('th', { class: 'num', text: 'Exceptions' }))));
      const tb = el('tbody');
      for (const a of run.arms || []) {
        const perSolve = a.solved > 0 ? a.total_cost_usd / a.solved : null;
        tb.appendChild(el('tr', { class: 'click', onclick: () => toggleBenchTasks(inner, run.id, a.arm) },
          el('td', {}, el('code', { text: a.arm })),
          el('td', { class: 'num', text: num(a.tasks) }),
          el('td', { class: 'num', text: num(a.solved) }),
          el('td', { class: 'num', text: pct(a.solve_rate * 100) }),
          el('td', { class: 'num', text: a.mean_reward.toFixed(3) }),
          el('td', { class: 'num', text: a.mean_steps.toFixed(1) }),
          el('td', { class: 'num', text: usd(a.total_cost_usd) }),
          el('td', { class: 'num', text: usd(a.mean_cost_usd) }),
          el('td', { class: 'num', text: perSolve === null ? '—' : usd(perSolve) }),
          el('td', { class: 'num', text: pct(a.cache_hit_rate * 100, 2) }),
          el('td', { class: 'num', text: dur(a.mean_wall_s * 1000) }),
          el('td', { class: 'num', text: num(a.exceptions) })));
      }
      tbl.appendChild(tb);
      inner.appendChild(el('div', { class: 'tblwrap' }, tbl));
      inner.appendChild(el('p', { class: 'note', text: 'Cost per solve is the number that matters: an arm that spends less by solving fewer tasks has not saved anything. Click an arm for its per-task rows.' }));
      // Cost-vs-reward scatter: the visualization the issue asks for.
      inner.appendChild(el('h2', { text: 'Cost vs reward, by arm' }));
      const scatter = el('div', { class: 'chart', 'data-testid': 'bench-scatter' });
      inner.appendChild(scatter);
      sec.appendChild(inner);
      host.appendChild(sec);
      renderScatter(scatter, run.arms || []);
    });
  } catch (err) {
    emptyState(host, 'Could not load benchmarks', String(err.message || err));
  }
}

function renderScatter(host, arms) {
  clear(host);
  const pts = arms.filter((a) => a.tasks > 0);
  if (!pts.length) { emptyState(host, 'No arms to plot', ''); return; }
  const { w, h, pad } = CH;
  const xMax = Math.max(...pts.map((a) => a.mean_cost_usd)) * 1.15 || 1;
  const svg = svgEl('svg', { viewBox: `0 0 ${w} ${h}`, role: 'img' });
  svg.setAttribute('aria-label', 'mean cost per task versus solve rate, by arm');
  const px = (v) => pad.l + (v / xMax) * (w - pad.l - pad.r);
  const py = (v) => h - pad.b - v * (h - pad.t - pad.b);
  for (const t of [0, 0.25, 0.5, 0.75, 1]) {
    svg.appendChild(svgEl('line', { class: 'gridline', x1: pad.l, x2: w - pad.r, y1: py(t), y2: py(t) }));
    const lab = svgEl('text', { class: 'axis-text', x: pad.l - 6, y: py(t) + 3, 'text-anchor': 'end' });
    lab.textContent = (t * 100).toFixed(0) + '%';
    svg.appendChild(lab);
  }
  svg.appendChild(svgEl('line', { class: 'axis', x1: pad.l, x2: w - pad.r, y1: h - pad.b, y2: h - pad.b }));
  for (const t of [0, xMax / 2, xMax]) {
    const lab = svgEl('text', { class: 'axis-text', x: px(t), y: h - pad.b + 14, 'text-anchor': 'middle' });
    lab.textContent = usd(t);
    svg.appendChild(lab);
  }
  pts.forEach((a, i) => {
    const cx = px(a.mean_cost_usd), cy = py(a.solve_rate);
    svg.appendChild(svgEl('circle', { cx, cy, r: 7, fill: `var(--s${(i % 5) + 1})`, opacity: '0.85' }));
    const lab = svgEl('text', { class: 'axis-text', x: cx + 11, y: cy + 4 });
    lab.textContent = a.arm;
    svg.appendChild(lab);
  });
  host.appendChild(svg);
  host.appendChild(el('div', { class: 'legend' },
    el('span', { text: 'x: mean billed cost per task  ·  y: solve rate  ·  up and to the left is better' })));
}

async function toggleBenchTasks(sec, runID, arm) {
  const existing = sec.querySelector('[data-tasks="' + arm + '"]');
  if (existing) { existing.remove(); return; }
  const host = el('div', { class: 'tblwrap', 'data-tasks': arm, 'data-testid': 'bench-tasks' });
  sec.appendChild(host);
  loadingState(host, 2);
  try {
    const { tasks } = await api('benchmarks/' + runID + '/tasks', { arm });
    clear(host);
    const tbl = el('table', { class: 'tbl compact' }, el('thead', {}, el('tr', {},
      el('th', { text: 'Task' }), el('th', { class: 'num', text: 'Reward' }),
      el('th', { class: 'num', text: 'Steps' }), el('th', { class: 'num', text: 'Cache r/w' }),
      el('th', { class: 'num', text: 'Fresh' }), el('th', { class: 'num', text: 'Out' }),
      el('th', { class: 'num', text: 'Cost' }), el('th', { class: 'num', text: 'Wall' }), el('th', { text: '' }))));
    const tb = el('tbody');
    for (const t of tasks) {
      tb.appendChild(el('tr', {},
        el('td', {}, el('span', { class: 'trunc', title: t.task, text: t.task })),
        el('td', { class: 'num', text: t.reward.toFixed(2) }),
        el('td', { class: 'num', text: num(t.steps) }),
        el('td', { class: 'num', text: compact(t.cache_read) + ' / ' + compact(t.cache_write) }),
        el('td', { class: 'num', text: compact(t.fresh_input) }),
        el('td', { class: 'num', text: compact(t.completion_tokens) }),
        el('td', { class: 'num', text: usd(t.cost_usd) }),
        el('td', { class: 'num', text: dur(t.wall_s * 1000) }),
        el('td', {}, t.exception ? el('span', { class: 'pill missing', text: 'exception' })
          : t.reward >= 1 ? el('span', { class: 'pill complete', text: 'solved' })
            : el('span', { class: 'pill neutral', text: 'unsolved' }))));
    }
    tbl.appendChild(tb);
    host.appendChild(tbl);
  } catch (err) {
    emptyState(host, 'Could not load tasks', String(err.message || err));
  }
}

// ── config ─────────────────────────────────────────────────────────────────
function renderTree(v, key) {
  if (v === null || v === undefined) return el('div', { class: 'v', text: '—' });
  if (Array.isArray(v)) {
    return el('div', {}, el('div', { class: 'k', text: key }),
      el('div', { class: 'v', text: v.map((x) => (typeof x === 'object' ? JSON.stringify(x) : String(x))).join(', ') || '(empty)' }));
  }
  if (typeof v === 'object') {
    const box = el('details', { class: 'diff', open: key === undefined ? 'open' : null },
      el('summary', { text: key === undefined ? 'effective configuration' : key }));
    const inner = el('div', { style: 'padding:10px 12px' });
    const grid = el('div', { class: 'kv' });
    for (const [k, val] of Object.entries(v)) {
      if (val !== null && typeof val === 'object' && !Array.isArray(val)) inner.appendChild(renderTree(val, k));
      else grid.appendChild(kv(k, Array.isArray(val) ? (val.join(', ') || '(empty)') : String(val)));
    }
    inner.insertBefore(grid, inner.firstChild);
    box.appendChild(inner);
    return box;
  }
  return el('div', {}, el('div', { class: 'k', text: key }), el('div', { class: 'v', text: String(v) }));
}

async function loadConfig() {
  const host = clear($('#config-body'));
  loadingState(host, 3);
  try {
    const cfg = await api('config');
    clear(host).appendChild(renderTree(cfg));
  } catch (err) {
    emptyState(host, err.status === 403 ? 'Configuration is not visible from this address'
      : 'Could not load configuration', String(err.message || err));
  }
  const chost = clear($('#capture-body'));
  try {
    const { capture: c, description } = await api('capture');
    chost.appendChild(el('div', { class: 'kv' },
      kv('Captured', num(c.captured)), kv('Written', num(c.written)),
      kv('Dropped', num(c.dropped)), kv('Insert errors', num(c.errors)),
      kv('Queue', c.queued + ' / ' + c.queue_cap), kv('SSE clients', num(c.sse_clients)),
      kv('Database', c.db_path || '(in memory — history is lost on restart)'),
      kv('Database size', compact(c.db_bytes) + ' B')));
    chost.appendChild(el('p', { class: 'note', text: description }));
  } catch (err) {
    emptyState(chost, 'Could not load capture health', String(err.message || err));
  }
}

// ── capture-drop + observe-mode banners ────────────────────────────────────
async function checkCapture() {
  try {
    const { capture: c } = await api('capture');
    const b = $('#capture-warning');
    if (c.dropped > 0) {
      b.textContent = `${num(c.dropped)} captured request(s) were dropped because the capture queue was full — ` +
        'the figures below under-report. Requests were never delayed; observability was. Raise the queue size.';
      b.hidden = false;
    } else b.hidden = true;

    // Observe mode has to be unmissable. Every request was forwarded UNTOUCHED, so
    // reading these figures as achieved savings is exactly the wrong conclusion — and it
    // is the conclusion a dashboard invites unless it says otherwise.
    const o = $('#observe-banner');
    if (c.mode === 'observe') {
      const q = c.observe_queue;
      let text = 'You are currently in OBSERVE mode. context-guru did not modify any request: ' +
        'every request above was forwarded to the provider untouched, and the pipeline ran ' +
        'off-path on a copy. Savings shown here are what compaction WOULD have achieved, ' +
        'not what it did.';
      if (q) {
        text += ` Off-path queue: ${num(q.processed)} measured, ${num(q.pending)} in flight`;
        // Drops matter more than depth: a dropped observation never happened, so the
        // projection understates. Say which direction the error runs.
        text += q.dropped > 0
          ? `, ${num(q.dropped)} DROPPED — the projection under-reports by whatever those would have saved.`
          : ', 0 dropped.';
      }
      o.textContent = text;
      o.hidden = false;
    } else o.hidden = true;
  } catch (_) { /* the banners are best-effort */ }
}

// ── views + filters ────────────────────────────────────────────────────────
const loaders = {
  overview: loadOverview, components: loadComponents, sessions: loadSessions,
  requests: loadRequests, benchmarks: loadBenchmarks, config: loadConfig,
};

function go(view) {
  if (!Object.prototype.hasOwnProperty.call(loaders, view)) view = 'overview';
  state.view = view;
  for (const t of $$('.tab')) t.setAttribute('aria-selected', String(t.dataset.view === view));
  for (const s of $$('.view')) s.hidden = s.id !== 'view-' + view;
  location.hash = view;
  loaders[view]();
}

function setFilter(key, value) {
  state.filter[key] = value;
  const ctl = $('#f-' + key);
  if (ctl) ctl.value = value;
  resetPaging();
}

function resetPaging() { state.reqCursor = 0; state.reqStack = []; state.sessOffset = 0; }

function readFilters() {
  state.filter = {
    q: $('#f-q').value.trim(), model: $('#f-model').value, provider: $('#f-provider').value,
    agent: $('#f-agent').value, preset: $('#f-preset').value, mode: $('#f-mode').value,
    component: $('#f-component').value, reason: $('#f-reason').value,
    accounting: $('#f-accounting').value, session: state.filter.session || '',
  };
  state.range = Number($('#f-range').value) || 0;
  resetPaging();
  loaders[state.view]();
}

async function loadFacets() {
  try {
    const f = await api('facets');
    for (const dim of ['model', 'provider', 'agent', 'preset', 'mode', 'component']) {
      const sel = $('#f-' + dim);
      const keep = sel.value;
      while (sel.options.length > 1) sel.remove(1);
      for (const v of f[dim] || []) sel.appendChild(el('option', { value: v }, v));
      sel.value = keep;
    }
  } catch (_) { /* dropdowns degrade to "All" */ }
}

// ── SSE ────────────────────────────────────────────────────────────────────
let lastEventID = 0;
function connectLive() {
  const src = new EventSource('/api/events' + (lastEventID ? '?last_event_id=' + lastEventID : ''));
  const label = $('#live-label'), box = $('.live');
  src.onopen = () => { box.className = 'live on'; label.textContent = 'live'; };
  src.onerror = () => { box.className = 'live off'; label.textContent = 'reconnecting…'; };
  src.addEventListener('request', (ev) => {
    let e;
    try { e = JSON.parse(ev.data); } catch (_) { return; }
    lastEventID = Math.max(lastEventID, e.id || 0);
    state.live.unshift(e);
    if (state.live.length > 60) state.live.length = 60;
    if (state.view === 'overview') renderLive();
  });
}

// ── boot ───────────────────────────────────────────────────────────────────
function initTheme() {
  const saved = localStorage.getItem('cg-theme');
  if (saved) document.documentElement.setAttribute('data-theme', saved);
  $('#theme').addEventListener('click', () => {
    const cur = document.documentElement.getAttribute('data-theme');
    const next = cur === 'dark' ? 'light' : cur === 'light' ? 'auto' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    if (next === 'auto') localStorage.removeItem('cg-theme');
    else localStorage.setItem('cg-theme', next);
  });
}

function init() {
  initTheme();
  for (const t of $$('.tab')) t.addEventListener('click', () => go(t.dataset.view));
  for (const id of ['f-model', 'f-provider', 'f-agent', 'f-preset', 'f-mode', 'f-component',
    'f-reason', 'f-accounting', 'f-range']) {
    $('#' + id).addEventListener('change', readFilters);
  }
  let deb;
  $('#f-q').addEventListener('input', () => { clearTimeout(deb); deb = setTimeout(readFilters, 250); });
  $('#f-clear').addEventListener('click', () => {
    for (const s of $$('.filters select')) s.value = s.id === 'f-range' ? '0' : '';
    $('#f-q').value = '';
    state.filter = {};
    readFilters();
  });
  $('#req-next').addEventListener('click', () => {
    if (!state.nextCursor) return;
    state.reqStack.push(state.reqCursor);
    state.reqCursor = state.nextCursor;
    loadRequests();
  });
  $('#req-prev').addEventListener('click', () => {
    state.reqCursor = state.reqStack.pop() || 0;
    loadRequests();
  });
  $('#sess-prev').addEventListener('click', () => { state.sessOffset = Math.max(0, state.sessOffset - 25); loadSessions(); });
  $('#sess-next').addEventListener('click', () => { state.sessOffset += 25; loadSessions(); });
  $('#bench-refresh').addEventListener('click', async () => {
    await fetch('/api/benchmarks?refresh=1');
    loadBenchmarks();
  });
  $('#drawer-close').addEventListener('click', closeDrawer);
  $('#scrim').addEventListener('click', closeDrawer);
  document.addEventListener('keydown', (ev) => { if (ev.key === 'Escape') closeDrawer(); });

  // An unknown or absent hash must land on Overview, not call loaders[undefined].
  const wanted = (location.hash || '').replace(/^#/, '');
  renderLive(); // show the feed's empty state immediately, not only on the first event
  go(Object.prototype.hasOwnProperty.call(loaders, wanted) ? wanted : 'overview');
  loadFacets();
  checkCapture();
  connectLive();
  // Poll the aggregates: SSE carries individual rows, but a rollup must be
  // recomputed server-side, and 10 s is well under a human's patience.
  setInterval(() => { if (state.view === 'overview') loadOverview(); }, 10000);
  setInterval(() => { loadFacets(); checkCapture(); }, 30000);
}

document.addEventListener('DOMContentLoaded', init);
