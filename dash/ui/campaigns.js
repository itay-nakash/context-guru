// Strategy campaigns: bulk-create/activate keep-alive strategies from a batch of
// KV-cache suggest cells (GET /api/kvcache/suggest), and see, per tenant, per
// hour-of-day, what each cell PREDICTED against what actually happened once it went
// live — see proxy/campaign.go for the full design.
//
// One appended file, self-mounted the same way kvcache.js and tools.js are: the tab,
// the section and the loader registration all happen here, so this feature is one
// line in the shared page (index.html's one added <script> tag). Manager-only, like
// Strategies, whose form/list/drawer conventions this borrows directly
// (loadStrategies/openStrategyLedger in app.js) — a campaign is a bulk, backtested way
// to create the same keepalive_strategies rows that page edits by hand. Every helper
// used here is app.js's own (el, clear, api, ctl, usd, num, when, tile, tileGroup,
// openDrawer, emptyState, errorState, loadingState) — no arithmetic happens in this
// file; every dollar figure arrives already computed from the server.
'use strict';

// ── mount ──────────────────────────────────────────────────────────────────
// Right after Strategies: a campaign is a bulk way to create the same rows that tab
// edits by hand.
(function mountCampaignsTab() {
  const tabs = $('.tabs');
  const tab = el('button', {
    role: 'tab', class: 'tab', 'data-view': 'campaigns', 'data-testid': 'tab-campaigns',
    'data-manager': '', hidden: 'hidden', 'aria-selected': 'false',
  }, 'Campaigns');
  const after = $('.tab[data-view="strategies"]', tabs);
  tabs.insertBefore(tab, after ? after.nextSibling : null);
})();

const campView = el('section', { class: 'view', id: 'view-campaigns', hidden: 'hidden' });
$('#main').appendChild(campView);

// ── local state ────────────────────────────────────────────────────────────
// Its own object, not app.js's shared filter state: this view is in UNFILTERED_VIEWS,
// the same as Strategies, since a campaign is a standing artifact, not a
// date-ranged view of live traffic.
const camp = {
  // The last suggest payload fetched live or read from an uploaded file, awaiting a
  // name before it becomes POST /api/keepalive/campaigns' body. Always submitted as
  // source:"upload" regardless of where it came from — see createCampaignFromPending's
  // own comment for why re-fetching at commit time would be the wrong choice.
  pending: null,
  // Bumped at the START of every drawer render (renderCampaignOverview,
  // renderCampaignTenantDrilldown) — not only when the drawer first opens — and checked
  // again once each one's fetch resolves, so a slow render that the manager has since
  // navigated away from (a different campaign, a different tenant, or back to the
  // overview — all inside the SAME open drawer) can never overwrite whatever's now
  // actually on screen.
  drawerGen: 0,
  // Bumped at the start of fetchLiveSuggestions and onUploadSuggestFile, and checked by
  // each before writing camp.pending or #camp-preview — so a slow live-fetch and a
  // faster file upload (or the reverse) started before it resolved can't stomp on
  // whichever one the manager is actually looking at. createCampaignFromPending reads
  // it too, so a create that finishes after the manager has already started a NEWER
  // preview doesn't clear that newer one out from under them.
  previewGen: 0,
  // Bumped at the start of refreshCampaignsList — same shape, for the campaigns list
  // table: whichever of two overlapping list refreshes resolves LAST otherwise wins,
  // regardless of which one was actually started last.
  listGen: 0,
};

async function loadCampaigns() {
  if (!campView.dataset.built) {
    buildCampaignsSkeleton(campView);
    campView.dataset.built = '1';
  }
  await refreshCampaignsList();
}

function buildCampaignsSkeleton(host) {
  host.appendChild(el('div', { class: 'panel' },
    el('h2', {}, 'New campaign'),
    el('p', { class: 'note' },
      'Bulk-create keep-alive strategies from a batch of KV-cache suggestions: fetch the ' +
      'live per-tenant, per-hour recommendations, or upload a JSON file in the same shape ' +
      '(GET /api/kvcache/suggest) to hand-edit one first. Only cells whose best strategy has ' +
      'a real enforcement path on this deployment are actually activated — everything else ' +
      'is still recorded, with a reason, never hidden. A fetched or uploaded payload can name ' +
      'any tenant, and creating from it targets exactly the tenants it names, regardless of ' +
      'who is uploading — there is no separate per-tenant confirmation step.'),
    el('div', { class: 'row-actions' },
      el('button', { class: 'ghost', 'data-testid': 'camp-fetch-live', onclick: fetchLiveSuggestions },
        'Fetch live suggestions'),
      el('span', { class: 'muted' }, 'or upload a file:'),
      el('input', {
        type: 'file', accept: 'application/json,.json', 'data-testid': 'camp-upload',
        onchange: onUploadSuggestFile,
      })),
    el('div', { id: 'camp-preview' })));
  host.appendChild(el('div', { class: 'panel' },
    el('div', { class: 'card-head' }, el('h2', {}, 'Campaigns'),
      el('span', { class: 'muted', id: 'campaigns-count' })),
    el('div', { id: 'campaigns-list' })));
}

// ── create flow: fetch or upload, preview, name, create ────────────────────

async function fetchLiveSuggestions() {
  const gen = ++camp.previewGen;
  const btn = $('[data-testid="camp-fetch-live"]');
  const preview = clear($('#camp-preview'));
  btn.disabled = true;
  loadingState(preview, 3);
  try {
    const suggest = await api('kvcache/suggest');
    if (gen !== camp.previewGen) return;
    camp.pending = suggest;
    renderSuggestPreview(clear(preview), suggest);
  } catch (e) {
    if (gen !== camp.previewGen) return;
    errorState(clear(preview), 'Could not fetch live suggestions', e);
  } finally {
    btn.disabled = false;
  }
}

function onUploadSuggestFile(ev) {
  const file = ev.target.files && ev.target.files[0];
  ev.target.value = ''; // lets the same file be chosen again after a fix
  if (!file) return;
  const gen = ++camp.previewGen;
  const preview = clear($('#camp-preview'));
  const reader = new FileReader();
  reader.onload = () => {
    if (gen !== camp.previewGen) return;
    let suggest;
    try {
      suggest = JSON.parse(reader.result);
    } catch (e) {
      errorState(clear(preview), 'That file is not valid JSON', e);
      return;
    }
    // JSON.parse("null") and JSON.parse("42") both succeed without throwing, so the
    // try/catch above alone does not guarantee an object with a .cells to read.
    if (!suggest || typeof suggest !== 'object' || Array.isArray(suggest)) {
      errorState(clear(preview), 'That file is valid JSON, but not a suggest object', '');
      return;
    }
    camp.pending = suggest;
    renderSuggestPreview(clear(preview), suggest);
  };
  reader.onerror = () => {
    if (gen !== camp.previewGen) return;
    errorState(clear(preview), 'Could not read that file', reader.error);
  };
  reader.readAsText(file);
}

/** renderSuggestPreview shows the raw suggest payload and a name field to create from it. */
function renderSuggestPreview(host, suggest) {
  const cells = suggest.cells || [];
  if (!cells.length) {
    emptyState(host, 'No cells in this suggestion set', 'Nothing to campaign over.');
    return;
  }
  const named = cells.filter((c) => c.user);
  const unnamed = cells.length - named.length;
  host.appendChild(el('p', { class: 'note' },
    `${named.length} cell(s) across ${(suggest.users || []).length} tenant(s), baseline ` +
    `"${suggest.baseline}", weekdays ${(suggest.weekdays_included || []).join(', ') || '—'}.` +
    (unnamed ? ` ${unnamed} cell(s) with no tenant id will be excluded from the campaign.` : '') +
    ' Which of these actually activate is decided when the campaign is created — this is ' +
    'the raw recommendation, not yet resolved against what this deployment can enforce.'));

  const SHOWN = 200;
  const tbl = el('table', { class: 'grid compact', 'data-testid': 'camp-preview-table' },
    el('thead', {}, el('tr', {},
      el('th', {}, 'Tenant'), el('th', { class: 'num' }, 'Hour (UTC)'),
      el('th', { class: 'num' }, 'Requests'), el('th', {}, 'Best strategy'),
      el('th', { class: 'num' }, 'Predicted saving'))));
  const body = el('tbody');
  for (const c of named.slice(0, SHOWN)) {
    body.appendChild(el('tr', {},
      el('td', {}, el('code', { class: 'clip' }, c.user)),
      el('td', { class: 'num' }, String(c.hour_utc)),
      el('td', { class: 'num' }, num(c.requests)),
      el('td', {}, c.best_strategy,
        c.insufficient_data ? el('span', { class: 'pill neutral' }, 'thin data') : null),
      el('td', { class: 'num' }, usd(c.saving_usd))));
  }
  tbl.appendChild(body);
  host.appendChild(el('div', { class: 'tblwrap', tabindex: '0' }, tbl));
  if (named.length > SHOWN) {
    host.appendChild(el('p', { class: 'muted small' }, `Showing the first ${SHOWN} of ${named.length} cells.`));
  }

  const nameInput = el('input', {
    type: 'text', maxlength: '64', placeholder: 'Campaign name', 'data-testid': 'camp-name',
  });
  const createBtn = el('button', {
    'data-testid': 'camp-create', onclick: () => createCampaignFromPending(nameInput, createBtn, host),
  }, 'Create campaign');
  host.appendChild(el('div', { class: 'row-actions', style: 'margin-top:var(--sp-3)' }, nameInput, createBtn));
}

/**
 * createCampaignFromPending always sends source:"upload" with the exact payload the
 * preview above rendered — even when that payload came from "Fetch live suggestions" —
 * rather than asking the server to re-fetch live data at commit time. Re-fetching would
 * let traffic between preview and commit change the numbers a manager just reviewed;
 * submitting exactly what was shown means "create this" always means what it says.
 *
 * createBtn is disabled synchronously, before the first await, and only re-enabled in a
 * finally — the same pattern fetchLiveSuggestions already uses — because a campaign
 * create has no idempotency/dedup on the server: two clicks (an accidental double-click,
 * or an impatient second click on a slow network) would each create a full, independent
 * set of live keep-alive strategies, not just a UI glitch.
 *
 * gen is captured (not bumped) at the start: starting a create does not itself start a
 * new preview, it commits the CURRENT one. But the manager isn't prevented from
 * starting a fresh fetch/upload while this POST is still in flight (only the Create
 * button itself is disabled) — if that happens, #camp-preview and camp.pending already
 * belong to that newer preview by the time this call resolves, and clearing/nulling
 * them here would wipe out work the manager has already moved on to, even though the
 * OLD campaign this call submitted was created successfully.
 */
async function createCampaignFromPending(nameInput, createBtn, previewHost) {
  const name = (nameInput.value || '').trim();
  if (!name) { nameInput.focus(); return; }
  const gen = camp.previewGen;
  const suggest = camp.pending;
  createBtn.disabled = true;
  try {
    const created = await ctl('/api/keepalive/campaigns', {
      method: 'POST',
      body: JSON.stringify({ name, source: 'upload', suggest }),
    });
    if (gen === camp.previewGen) {
      camp.pending = null;
      renderCreatedResult(clear(previewHost), created);
    }
    await refreshCampaignsList();
  } catch (e) {
    if (gen === camp.previewGen) errorState(previewHost, 'Could not create this campaign', e);
  } finally {
    createBtn.disabled = false;
  }
}

function renderCreatedResult(host, created) {
  const cells = created.cells || [];
  const activated = cells.filter((c) => c.activatable).length;
  const skipped = cells.length - activated;
  host.appendChild(el('div', { class: 'banner ok' },
    `Campaign "${created.name}" created — ${activated} cell(s) activated, ` +
    `${skipped} recorded but not activated.`));
  if (!skipped) return;
  const byReason = {};
  for (const c of cells) {
    if (c.activatable) continue;
    byReason[c.skip_reason] = (byReason[c.skip_reason] || 0) + 1;
  }
  const list = el('ul', { class: 'muted small' });
  for (const [reason, n] of Object.entries(byReason)) {
    list.appendChild(el('li', {}, `${n}× — ${reason}`));
  }
  host.appendChild(list);
}

// ── list ─────────────────────────────────────────────────────────────────

async function refreshCampaignsList() {
  const gen = ++camp.listGen;
  const host = clear($('#campaigns-list'));
  loadingState(host, 2);
  try {
    const out = await ctl('/api/keepalive/campaigns');
    if (gen !== camp.listGen) return;
    const rows = out.campaigns || [];
    $('#campaigns-count').textContent = `${rows.length} campaign${rows.length === 1 ? '' : 's'}`;
    renderCampaignsList(clear(host), rows);
  } catch (e) {
    if (gen !== camp.listGen) return;
    errorState(clear(host), 'Could not list campaigns', e);
  }
}

function renderCampaignsList(host, rows) {
  if (!rows.length) {
    emptyState(host, 'No campaigns yet', 'Create one above.');
    return;
  }
  const tbl = el('table', { class: 'grid', 'data-testid': 'campaigns-table' },
    el('thead', {}, el('tr', {},
      el('th', {}, 'Name'), el('th', {}, 'Source'), el('th', {}, 'Created'), el('th', {}, 'State'),
      el('th', {}, el('span', { class: 'vh' }, 'Row actions')))));
  const body = el('tbody');
  for (const c of rows) {
    body.appendChild(el('tr', { class: c.status === 'archived' ? 'revoked' : '' },
      el('td', {}, c.name),
      el('td', {}, c.source === 'suggest-live' ? 'live fetch' : 'upload'),
      el('td', {}, when(c.created_at)),
      el('td', {}, el('span', { class: 'pill ' + (c.status === 'active' ? 'complete' : 'partial') }, c.status)),
      el('td', {}, el('div', { class: 'row-actions' },
        el('button', { class: 'ghost small', onclick: () => openCampaignDrawer(c) }, 'Stats'),
        c.status === 'active'
          ? el('button', { class: 'ghost small', onclick: () => archiveCampaign(c) }, 'Archive')
          : null))));
  }
  tbl.appendChild(body);
  host.appendChild(el('div', { class: 'tblwrap', tabindex: '0' }, tbl));
}

async function archiveCampaign(c) {
  if (!confirm(`Archive "${c.name}"? Its strategies keep running — this only stops the ` +
    'campaign itself from being managed as a group.')) return;
  try {
    await ctl('/api/keepalive/campaigns/' + c.id, { method: 'DELETE' });
    await refreshCampaignsList();
  } catch (e) { alert(e.message); }
}

// ── drawer: aggregate overview, and a per-tenant drill-down in place of it ─

/** openStrategyLedger's per-entity precedent, for a whole campaign instead of one strategy. */
async function openCampaignDrawer(c) {
  const body = openDrawer('Campaign: ' + c.name, null);
  await renderCampaignOverview(body, c.id);
}

/**
 * Bumps camp.drawerGen itself, at the very start, rather than taking a generation from
 * the caller: #drawer-body is one singleton node the drawer reuses across opens AND
 * across in-drawer navigation (a tenant drill-down, "back to overview", a different
 * campaign entirely) — closing it, or navigating away from it, does not cancel an
 * in-flight fetch. A gen threaded in from the CALLER (as this used to do) is only fresh
 * at drawer-open; two navigations inside the same open drawer would share it and could
 * still race. Bumping here means every render, including a second one inside the same
 * drawer session, gets its own token.
 */
async function renderCampaignOverview(body, campaignID) {
  const gen = ++camp.drawerGen;
  clear(body);
  loadingState(body, 3);
  let detail;
  try {
    detail = await ctl('/api/keepalive/campaigns/' + campaignID);
  } catch (e) {
    if (gen !== camp.drawerGen) return;
    clear(body);
    errorState(body, 'Could not load this campaign', e);
    return;
  }
  if (gen !== camp.drawerGen) return;
  clear(body);
  body.appendChild(tileGroup(null, null, [
    tile('camp-predicted', 'Predicted saving', usd(detail.total_predicted_usd)),
    // The exact ceiling on the SAME cells Predicted sums: what a policy with perfect
    // foreknowledge of every next request would have saved, frozen at campaign-creation
    // time right alongside Predicted (see dash.KVCacheSuggestion.OptimalSavingUSD) — how
    // much headroom remains beyond what this campaign's own choice of arm captures.
    tile('camp-oracle', 'Oracle ceiling (predicted)', usd(detail.total_optimal_saving_usd)),
    tile('camp-real-saved', 'Real saved (ceiling)', usd(detail.total_real_saved_usd)),
    tile('camp-real-net', 'Real net', usd(detail.total_real_net_usd), null,
      detail.total_real_net_usd < 0 ? 'bad' : 'good'),
    tile('camp-tenants', 'Tenants', num((detail.tenants || []).length)),
  ]));
  if (detail.caveat) body.appendChild(el('p', { class: 'note' }, detail.caveat));
  const tenants = detail.tenants || [];
  if (!tenants.length) {
    emptyState(body, 'No tenants in this campaign', '');
    return;
  }
  body.appendChild(el('p', { class: 'note' }, 'Click a tenant for every hour cell’s ' +
    'historical/predicted saving alongside what actually happened.'));
  const tbl = el('table', { class: 'grid', 'data-testid': 'camp-tenants-table' },
    el('thead', {}, el('tr', {},
      el('th', {}, 'Tenant'), el('th', { class: 'num' }, 'Predicted'),
      el('th', { class: 'num' }, 'Real ping cost'), el('th', { class: 'num' }, 'Real saved'),
      el('th', { class: 'num' }, 'Real net'))));
  const tbody = el('tbody');
  for (const t of tenants) {
    tbody.appendChild(el('tr', {
      class: 'click', onclick: () => renderCampaignTenantDrilldown(body, campaignID, t.tenant_id),
    },
      el('td', {}, el('code', { class: 'clip' }, t.tenant_id)),
      el('td', { class: 'num' }, usd(t.predicted_usd)),
      el('td', { class: 'num' }, usd(t.real_ping_usd)),
      el('td', { class: 'num' }, usd(t.real_saved_usd)),
      el('td', { class: 'num ' + (t.real_net_usd < 0 ? 'bad-text' : 'good-text') }, usd(t.real_net_usd))));
  }
  tbl.appendChild(tbody);
  body.appendChild(el('div', { class: 'tblwrap', tabindex: '0' }, tbl));
}

/**
 * hourUTCToLocalLabel shows both the UTC hour a window actually fires at (windows are
 * stored in UTC, matching exactly the hour suggest computed each cell's saving from —
 * see proxy/campaign.go) and today's real Asia/Jerusalem equivalent, computed from
 * TODAY'S actual date rather than a fixed one so the DST offset shown is the one
 * genuinely in effect right now, not a guess about which regime some other day is in.
 */
function hourUTCToLocalLabel(hourUTC) {
  const now = new Date();
  const atHour = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), hourUTC, 0, 0));
  const local = new Intl.DateTimeFormat(undefined, {
    timeZone: 'Asia/Jerusalem', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(atHour);
  return `${String(hourUTC).padStart(2, '0')}:00 UTC (${local} Jerusalem today)`;
}

/** See renderCampaignOverview's doc comment — bumps its own generation, same reason. */
async function renderCampaignTenantDrilldown(body, campaignID, tenantID) {
  const gen = ++camp.drawerGen;
  clear(body);
  loadingState(body, 3);
  let out;
  try {
    out = await ctl('/api/keepalive/campaigns/' + campaignID + '/tenants/' + encodeURIComponent(tenantID));
  } catch (e) {
    if (gen !== camp.drawerGen) return;
    clear(body);
    errorState(body, 'Could not load this tenant’s drill-down', e);
    return;
  }
  if (gen !== camp.drawerGen) return;
  clear(body);
  body.appendChild(el('button', {
    class: 'ghost small', 'data-testid': 'camp-back-to-overview',
    onclick: () => renderCampaignOverview(body, campaignID),
  }, '← Back to overview'));
  body.appendChild(el('h3', {}, 'Tenant: ' + tenantID));
  if (out.caveat) body.appendChild(el('p', { class: 'note' }, out.caveat));
  const cells = out.cells || [];
  if (!cells.length) {
    emptyState(body, 'No cells for this tenant', '');
    return;
  }
  const tbl = el('table', { class: 'grid compact', 'data-testid': 'camp-drilldown-table' },
    el('thead', {}, el('tr', {},
      el('th', {}, 'Hour'), el('th', {}, 'Best strategy'), el('th', {}, 'State'),
      el('th', { class: 'num' }, 'Historical/predicted'), el('th', { class: 'num' }, 'Real saved'),
      el('th', { class: 'num' }, 'Real net'), el('th', { class: 'num' }, '$/1k requests'),
      el('th', { class: 'num' }, '$/active day'))));
  const tbody = el('tbody');
  for (const cell of cells) {
    const hasReal = cell.real_requests > 0;
    tbody.appendChild(el('tr', {},
      el('td', {}, hourUTCToLocalLabel(cell.hour_utc)),
      el('td', {}, cell.arm),
      el('td', {},
        cell.activatable
          ? el('span', { class: 'pill complete' }, 'active')
          : el('span', { class: 'pill missing', title: cell.skip_reason || '' }, 'not activated')),
      el('td', { class: 'num' }, usd(cell.predicted_usd)),
      el('td', { class: 'num' }, hasReal ? usd(cell.real_saved_usd) : el('span', { class: 'pill neutral' }, 'no data yet')),
      el('td', { class: 'num ' + (hasReal && cell.real_net_usd < 0 ? 'bad-text' : 'good-text') },
        hasReal ? usd(cell.real_net_usd) : '—'),
      // != null, not truthy: the rate is a JSON pointer field on the server
      // (proxy/campaign.go's *float64), sent only when its denominator was nonzero —
      // but the rate ITSELF can legitimately be exactly 0 (real traffic, $0 credited
      // that hour), which a truthy check would wrongly render as "—" (no data).
      el('td', { class: 'num' },
        cell.real_saved_usd_per_1k_requests != null ? usd(cell.real_saved_usd_per_1k_requests) : '—'),
      el('td', { class: 'num' },
        cell.real_saved_usd_per_active_day != null ? usd(cell.real_saved_usd_per_active_day) : '—')));
  }
  tbl.appendChild(tbody);
  body.appendChild(el('div', { class: 'tblwrap', tabindex: '0' }, tbl));
}

// ── wiring ─────────────────────────────────────────────────────────────────
Object.assign(loaders, { campaigns: loadCampaigns });
UNFILTERED_VIEWS.add('campaigns');
