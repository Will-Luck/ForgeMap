// Live updates over SSE. Imported only once the page is shown (never while
// prerendering). Swaps server fragments, raises toasts, keeps the header fresh.
import { el, icon, ago, fragment, transition, pulse, motionOK, signedOut, signInHere } from "./util.js";
import { refreshTimes } from "./time.js";

const status = document.getElementById("live-status");
const statusWord = status?.querySelector("[data-status-word]");
const liveTime = document.getElementById("live-time");
const srcList = document.querySelector("[data-src-list]");
const toasts = document.getElementById("toasts");
let failing = status?.dataset.status === "warn";
let failWord = statusWord?.textContent || "Live";

// The server names each source in the tick (forge names, the notes label).
const label = (s) => s.label || s.name || "Unknown";

function setStatus(state, word) {
  if (!status) return;
  status.dataset.status = state;
  if (statusWord) statusWord.textContent = word;
}

async function fetchFragment(url) {
  const r = await fetch(url, { headers: { Accept: "text/html" } });
  if (signedOut(r)) throw new Error("signed out");
  if (!r.ok) throw new Error("HTTP " + r.status);
  return fragment(await r.text());
}

// Open disclosures, keyed by their nearest id inside the swapped element and
// their place under it (a note or an idea can hold more than one).
function openKeys(root) {
  const keys = [];
  for (const d of root.querySelectorAll("details[open]")) {
    const owner = d.parentElement?.closest("[id]");
    if (!owner || !root.contains(owner)) continue;
    keys.push([owner.id, [...owner.querySelectorAll("details")].indexOf(d)]);
  }
  return keys;
}

function reopen(root, keys) {
  for (const [id, i] of keys) root.querySelector("#" + CSS.escape(id))?.querySelectorAll("details")[i]?.setAttribute("open", "");
}

const FOCUSABLE = "a[href], button, input, select, textarea, summary, [tabindex]";

// Where focus sits inside the old element: by id, else by href (and which of
// the links with that href), else by its place among the focusable things.
function focusKey(root) {
  const a = document.activeElement;
  if (!a || a === root || !root.contains(a)) return null;
  const href = a.getAttribute("href");
  return {
    id: a.id || null,
    href,
    nth: href ? [...root.querySelectorAll(`[href="${CSS.escape(href)}"]`)].indexOf(a) : -1,
    i: [...root.querySelectorAll(FOCUSABLE)].indexOf(a),
  };
}

function restoreFocus(root, key) {
  if (!key) return;
  let t = key.id ? root.querySelector("#" + CSS.escape(key.id)) : null;
  if (!t && key.href) t = root.querySelectorAll(`[href="${CSS.escape(key.href)}"]`)[Math.max(key.nth, 0)];
  if (!t && key.i >= 0) t = root.querySelectorAll(FOCUSABLE)[key.i];
  t?.focus({ preventScroll: true });
}

// The newest event per element; an older response that lands later is dropped.
const latest = new Map();
// The fetch in flight per element, with the hash it was asked for: a second
// ask for that hash (a project event, then the board check) joins it.
const pending = new Map();

// Where each kind of live element is fetched from: a project's card or
// panels, or the overview's head.
function fragmentURL(kind, slug) {
  if (kind === "head") return "/fragments/overview-head";
  return "/p/" + encodeURIComponent(slug) + (kind === "card" ? "/card" : "/live");
}

// Swap in an element whose hash moved. True once the page holds it (or has
// nothing to swap), false when the fetch failed, so the caller can try again.
// ordered: the ask is a project event, which the stream delivers in order.
async function swap(kind, slug, hash, ordered) {
  const sel = `[data-live-${kind}="${CSS.escape(slug)}"]`;
  const key = kind + " " + slug;
  const seen = document.querySelector(sel);
  if (!seen) return true;
  const run = pending.get(key);
  if (seen.dataset.hash === hash) {
    // A project event is the newest word on its project, so a fetch still
    // in flight for another hash is older: its answer is dropped when it
    // lands. A board check or a catch-up read can be older than an event
    // that came while it was read, so it drops nothing.
    if (ordered && run && run.hash !== hash) {
      latest.set(key, (latest.get(key) || 0) + 1);
      pending.delete(key);
    }
    return true;
  }
  if (run && run.hash === hash) return run.done;
  const n = (latest.get(key) || 0) + 1;
  latest.set(key, n);
  const done = fetchSwap(kind, slug, sel, key, n, seen);
  pending.set(key, { hash, done });
  try {
    return await done;
  } finally {
    if (pending.get(key)?.done === done) pending.delete(key);
  }
}

async function fetchSwap(kind, slug, sel, key, n, seen) {
  let frag;
  try {
    frag = await fetchFragment(fragmentURL(kind, slug));
  } catch {
    return false; // the next change retries
  }
  // A newer ask took over: its answer is the one that counts.
  if (latest.get(key) !== n) return pending.get(key)?.done ?? false;
  const next = frag.querySelector(sel);
  if (!next) return false;
  // Look the element up again inside the update: an earlier swap may have
  // replaced the node seen before the fetch. The update runs a frame later,
  // so a newer ask may have started meanwhile: its answer is the one that
  // counts.
  await transition(() => {
    if (latest.get(key) !== n) return;
    const old = document.querySelector(sel);
    if (!old) return;
    reopen(next, openKeys(old));
    const focus = focusKey(old);
    old.replaceWith(next);
    if (kind === "head") placeEmpty(frag);
    // Move a card whose state changed before focus goes back into it:
    // moving a focused node drops its focus.
    if (kind === "card") place(next);
    restoreFocus(next, focus);
    // Count again on every card swap: its state or its category may differ.
    if (kind === "card") recount();
  }, document.querySelector(sel) || seen);
  if (next.isConnected && kind !== "head") pulse(kind === "card" ? next : next.querySelector(".headline") || next);
  return true;
}

// The overview's board follows the site: a card whose state changed moves to
// that state's section, a project added after load is fetched and put in
// its section, one removed is taken out, and every count is taken again
// from the cards. A reload shows the same.
const board = document.getElementById("board");
const cardSel = (slug) => `[data-live-card="${CSS.escape(slug)}"]`;

// The server's order within a section: newest activity first, then name.
function goesBefore(a, b) {
  const la = Number(a.dataset.last);
  const lb = Number(b.dataset.last);
  if (la !== lb) return la > lb;
  const name = (c) => (c.querySelector(".card-name")?.textContent || "").toLowerCase();
  return name(a) < name(b);
}

// Put a card in its state's section, in order, unless it is in it already.
// True when it moved.
function place(card) {
  const cards = board?.querySelector(`#tier-${CSS.escape(card.dataset.tier || "")} .cards`);
  if (!cards || card.parentElement === cards) return false;
  const after = [...cards.children].find((c) => c !== card && goesBefore(card, c));
  cards.insertBefore(card, after || null);
  return true;
}

// The empty board's note comes with the head: the server draws it while the
// site has no project, with the forges' words of the moment (the head hash
// covers them then), so a fixed token shows without a reload, and the note
// goes with the first project and comes back, current, after the last.
function placeEmpty(frag) {
  if (!board) return;
  const old = board.querySelector("[data-board-empty]");
  const next = frag.querySelector("[data-board-empty]");
  if (old && next) old.replaceWith(next);
  else if (next) board.querySelector(".board-top")?.after(next);
  else old?.remove();
  recount();
}

// Each section's counts (all, and per category), the board's and the
// category filter's, from the cards on the page. A section with no card is
// hidden, as the server renders it, and so is the empty board's note while
// the board has a card (one can land before the head that drops the note).
function recount() {
  if (!board) return;
  const keys = [...document.querySelectorAll('input[name="cat"]')].map((i) => i.value).filter(Boolean);
  const all = [...board.querySelectorAll(".card-wrap")];
  const empty = board.querySelector("[data-board-empty]");
  if (empty) empty.hidden = all.length > 0;
  for (const sec of board.querySelectorAll("details.tier")) {
    const cards = [...sec.querySelectorAll(".card-wrap")];
    const sum = sec.querySelector("summary");
    sec.hidden = cards.length === 0;
    const total = sum.querySelector('.count[data-n="all"]');
    if (total) total.textContent = cards.length;
    for (const k of keys) {
      const n = cards.filter((c) => c.dataset.cat === k).length;
      let span = sum.querySelector(`.count[data-n="${CSS.escape(k)}"]`);
      if (!n) {
        span?.remove();
        continue;
      }
      if (!span) {
        span = el("span", { class: "count", "data-n": k });
        sum.querySelector(".tier-explain")?.before(span);
      }
      span.textContent = n;
    }
  }
  const set = (sel, n) => {
    const node = document.querySelector(sel);
    if (node) node.textContent = n;
  };
  set("#board-h .count", all.length);
  set('label[for="cat-all"] .n', all.length);
  for (const k of keys) set(`label[for="cat-${CSS.escape(k)}"] .n`, all.filter((c) => c.dataset.cat === k).length);
}

// A project the board has no card for: fetch its card and put it in place.
// True once the board holds it, false when the fetch failed. A second ask
// while the fetch is in flight joins it.
const adding = new Map();
function addCard(slug) {
  if (!board || board.querySelector(cardSel(slug))) return Promise.resolve(true);
  if (!adding.has(slug)) adding.set(slug, fetchCard(slug).finally(() => adding.delete(slug)));
  return adding.get(slug);
}

async function fetchCard(slug) {
  let frag;
  try {
    frag = await fetchFragment("/p/" + encodeURIComponent(slug) + "/card");
  } catch {
    return false; // the next check tries again
  }
  const card = frag.querySelector("[data-live-card]");
  if (!card) return false;
  if (board.querySelector(cardSel(slug))) return true;
  await transition(() => {
    if (!board.querySelector(cardSel(slug)) && place(card)) recount();
  }, board);
  if (card.isConnected) pulse(card);
  return true;
}

// Check the board against the site's projects (live.json's hashes): take
// out the cards of projects that are gone, swap in the ones whose hash moved
// (their project event can be missed: a full buffer, or a change before the
// stream opened) and fetch the ones that are new. True once every card it
// fetched is in place.
async function reconcile(hashes) {
  if (!board) return true;
  const gone = [...board.querySelectorAll("[data-live-card]")].filter((c) => !Object.hasOwn(hashes, c.dataset.liveCard));
  if (gone.length) {
    transition(() => {
      for (const c of gone) c.remove();
      recount();
    }, gone[0]);
  }
  const runs = [];
  for (const [slug, hash] of Object.entries(hashes)) runs.push(swap("card", slug, hash), addCard(slug));
  return (await Promise.all(runs)).every(Boolean);
}

// The overview hash the board was last checked against, apart from the
// head's: the head can swap in while the board check fails, and the next
// tick must still check the board. The page renders both from one snapshot.
// It moves on only once every card fetch of the check has landed, so a
// failed one is tried again at the next tick.
let boardHash = document.querySelector("[data-live-head]")?.dataset.hash || "";
// Only the newest check may apply; an older answer that lands later is dropped.
let boardCheck = 0;

async function checkBoard(n, hash, hashes) {
  if (await reconcile(hashes) && n === boardCheck) boardHash = hash;
}

async function syncBoard() {
  const n = ++boardCheck;
  try {
    const r = await fetch("/api/live.json");
    if (signedOut(r) || !r.ok) return;
    const state = await r.json();
    if (n !== boardCheck) return;
    await checkBoard(n, state.overview || "", state.hashes || {});
  } catch {
    // boardHash stays as it was, so the next tick checks again
  }
}

// The overview head's hash moved on: swap the head in and, until a check
// succeeds, check the board, whose membership the hash also covers.
function onOverview(hash, hashes) {
  const head = document.querySelector("[data-live-head]");
  if (!hash || !head) return;
  if (head.dataset.hash !== hash) swap("head", head.dataset.liveHead, hash);
  if (hash === boardHash) return;
  if (hashes) checkBoard(++boardCheck, hash, hashes);
  else syncBoard();
}

function onProject(data, ordered) {
  if (!data.slug) return;
  swap("card", data.slug, data.hash, ordered);
  swap("panels", data.slug, data.hash, ordered);
  addCard(data.slug);
}

function toast({ text, href, icon: name, level = "info", project, slug, at }) {
  if (!toasts) return;
  while (toasts.children.length >= 3) toasts.firstElementChild.remove();
  const body = el("div", { class: "toast-body" });
  if (project) body.append(href ? el("a", { href }, project) : el("b", null, project));
  body.append(el("span", { class: "toast-text" }, text));
  if (at) body.append(el("span", { class: "muted small" }, ago(at)));
  const close = el("button", { type: "button", class: "icon-btn", "aria-label": "Dismiss" }, icon("x"));
  const cat = slug ? document.querySelector(`[data-slug="${CSS.escape(slug)}"]`)?.dataset.cat : null;
  const lead = project ? el("span", { class: "tile sm", "data-cat": cat || null }, icon(name)) : el("span", { class: "pal-ic" }, icon(name || "info"));
  const node = el("div", { class: "toast lv-" + level }, lead, body, close);
  let remaining = 6000;
  let started = Date.now();
  let timer = setTimeout(dismiss, remaining);
  function dismiss() {
    clearTimeout(timer);
    if (!motionOK()) { node.remove(); return; }
    node.classList.add("leaving");
    setTimeout(() => node.remove(), 300);
  }
  node.addEventListener("pointerenter", () => {
    clearTimeout(timer);
    remaining -= Date.now() - started;
  });
  node.addEventListener("pointerleave", () => {
    started = Date.now();
    timer = setTimeout(dismiss, Math.max(remaining, 1500));
  });
  close.addEventListener("click", dismiss);
  toasts.append(node);
}

// The site's time zone, as the server writes its own titles in.
let siteTime;
function zoned(t) {
  if (!siteTime) {
    const opts = { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" };
    try {
      siteTime = new Intl.DateTimeFormat("en-GB", { ...opts, timeZone: document.body.dataset.tz || "UTC" });
    } catch {
      siteTime = new Intl.DateTimeFormat("en-GB", { ...opts, timeZone: "UTC" });
    }
  }
  return siteTime.format(t);
}

// A <time data-ago> like the server's ago helper, so time.js keeps it fresh.
// A zero or missing time reads "never", as on the server.
function stamp(iso) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t) || t <= 0) return "never";
  return el("time", { datetime: iso, "data-ago": true, title: zoned(t).replace(" at ", ", ") }, ago(iso));
}

function onTick(data) {
  if (liveTime && data.refreshed_at) {
    liveTime.setAttribute("datetime", data.refreshed_at);
    liveTime.textContent = ago(data.refreshed_at);
  }
  const sources = Array.isArray(data.sources) ? data.sources : [];
  const bad = sources.filter((s) => !s.ok);
  failing = bad.length > 0;
  failWord = bad.length === 1 ? label(bad[0]) + " failing" : bad.length + " sources failing";
  setStatus(failing ? "warn" : "ok", failing ? failWord : "Live");
  if (srcList && sources.length) {
    srcList.replaceChildren(
      ...sources.map((s) =>
        el(
          "li",
          { class: s.ok ? "ok" : "bad" },
          icon(s.ok ? "check" : "octagon-alert"),
          el(
            "span",
            null,
            el("b", null, label(s)),
            " " + (s.ok ? "refreshed " : "failing, last good "),
            stamp(s.refreshed_at),
            s.error ? el("span", { class: "src-err" }, s.error) : null,
            s.note ? el("span", { class: "src-note" }, s.note) : null,
          ),
        ),
      ),
    );
  }
  onOverview(data.overview, data.hashes);
}

const parse = (e) => {
  try { return JSON.parse(e.data); } catch { return null; }
};

// While the stream was down (an error, or the page sat in the back/forward
// cache) changes went by unseen: read the current state once, refresh the
// header and swap in every block whose hash moved on.
async function catchUp() {
  let state;
  try {
    const r = await fetch("/api/live.json");
    if (signedOut(r) || !r.ok) return;
    state = await r.json();
  } catch {
    return;
  }
  onTick(state);
  const hashes = state.hashes || {};
  const slugs = new Set([...document.querySelectorAll("[data-live-card], [data-live-panels]")].map((n) => n.dataset.liveCard || n.dataset.livePanels));
  for (const slug of slugs) if (hashes[slug]) onProject({ slug, hash: hashes[slug] });
}

let es = null;
let missed = false;

// A stream the browser gave up on (a proxy answering 502 while the app is
// down closes an EventSource for good) is opened again after 5 s, doubling
// to at most 60 s, each wait moved by up to a fifth either way so the open
// tabs do not all come back at once. After 2 minutes down the header says
// "Offline, retrying" and the sources list offers a reload.
const RETRY_FIRST = 5000;
const RETRY_MAX = 60000;
const OFFLINE_AFTER = 2 * 60 * 1000;
const srcPop = document.getElementById("src-pop");
let retryWait = 0;
let retryTimer = 0;
let offlineTimer = 0;
let offline = false;
let reloadRow = null;

function setOffline(on) {
  offline = on;
  if (on && !reloadRow && srcPop) {
    reloadRow = el(
      "p",
      { class: "src-reload" },
      icon("refresh-cw"),
      el("span", null, el("b", null, "Offline"), ", retrying. ", el("a", { href: location.pathname + location.search }, "Reload")),
    );
    srcPop.append(reloadRow);
  } else if (!on && reloadRow) {
    reloadRow.remove();
    reloadRow = null;
  }
}

// The stream is down: say so, and start the 2-minute clock once.
function lost() {
  missed = true;
  if (!offline && !offlineTimer) {
    offlineTimer = setTimeout(() => {
      offlineTimer = 0;
      setOffline(true);
      setStatus("reconnecting", "Offline, retrying");
    }, OFFLINE_AFTER);
  }
  setStatus("reconnecting", offline ? "Offline, retrying" : "Reconnecting");
}

// The stream is up, or the page is leaving: forget the failure.
function settle() {
  clearTimeout(offlineTimer);
  offlineTimer = 0;
  clearTimeout(retryTimer);
  retryTimer = 0;
  retryWait = 0;
  setOffline(false);
}

function connect() {
  if (!window.EventSource || es) return;
  clearTimeout(retryTimer);
  retryTimer = 0;
  const source = new EventSource("/events");
  es = source;
  // The server ends each stream after 30 minutes and says so first (renew):
  // the browser opens the next one a second later, which is not a failure.
  let renewing = false;
  source.addEventListener("renew", () => { renewing = true; });
  // The server ends a stream whose viewer may no longer see the page.
  source.addEventListener("signedout", () => {
    source.close();
    if (es === source) es = null;
    signedOut({ status: 401 });
  });
  source.addEventListener("open", () => {
    settle();
    setStatus(failing ? "warn" : "ok", failing ? failWord : "Live");
    if (missed) {
      missed = false;
      catchUp();
    }
  });
  source.addEventListener("error", () => {
    if (renewing && source.readyState === EventSource.CONNECTING) {
      // Keep the header as it is, and read what went by in the gap once
      // the next stream is open.
      renewing = false;
      missed = true;
      return;
    }
    renewing = false;
    lost();
    // CONNECTING: the browser is retrying by itself.
    if (source.readyState !== EventSource.CLOSED) return;
    source.close();
    if (es === source) es = null;
    const retry = (out) => {
      if (out) return;
      retryWait = retryWait ? Math.min(retryWait * 2, RETRY_MAX) : RETRY_FIRST;
      retryTimer = setTimeout(connect, Math.round(retryWait * (0.8 + Math.random() * 0.4)));
    };
    // A stream refused with 401 closes for good, like one a proxy refused.
    // Where the install has its own sign-in, ask once which it was (for 5 s
    // at most, so a slow proxy cannot hold the retry back) before trying
    // again; elsewhere just try again.
    if (!signInHere()) return retry(false);
    fetch("/api/live.json", { method: "HEAD", signal: AbortSignal.timeout?.(5000) }).then(signedOut, () => false).then(retry);
  });
  source.addEventListener("project", (e) => { const d = parse(e); if (d) onProject(d, true); });
  source.addEventListener("tick", (e) => { const d = parse(e); if (d) onTick(d); });
  source.addEventListener("activity", (e) => {
    const ev = parse(e);
    if (!ev) return;
    toast({ text: ev.text, href: ev.href || (ev.project ? "/p/" + ev.project : null), icon: ev.icon, level: ev.level, project: ev.project_name, slug: ev.project, at: ev.at });
  });
}

// Closing on pagehide lets the page into the back/forward cache. A restored
// page does not run its modules again, so open the stream again on pageshow;
// the header says "Reconnecting" until it is back.
window.addEventListener("pagehide", () => {
  es?.close();
  es = null;
  missed = true;
  settle();
});
window.addEventListener("pageshow", (e) => {
  if (!e.persisted) return;
  refreshTimes();
  setStatus("reconnecting", "Reconnecting");
  connect();
});
connect();

document.addEventListener("forgemap:toast", (e) => toast(e.detail || {}));
