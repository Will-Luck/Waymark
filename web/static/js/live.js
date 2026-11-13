// Live updates over SSE. Imported only once the page is shown (never while
// prerendering). Swaps server fragments, raises toasts, keeps the header fresh.
import { el, icon, ago, fragment, transition, pulse, motionOK } from "./util.js";
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

async function swap(kind, slug, hash) {
  const sel = `[data-live-${kind}="${CSS.escape(slug)}"]`;
  const key = kind + " " + slug;
  const n = (latest.get(key) || 0) + 1;
  latest.set(key, n);
  const seen = document.querySelector(sel);
  if (!seen || seen.dataset.hash === hash) return;
  let frag;
  try {
    frag = await fetchFragment("/p/" + encodeURIComponent(slug) + (kind === "card" ? "/card" : "/live"));
  } catch {
    return; // the next change retries
  }
  if (latest.get(key) !== n) return;
  const next = frag.querySelector(sel);
  if (!next) return;
  // Look the element up again inside the update: an earlier swap may have
  // replaced the node seen before the fetch.
  await transition(() => {
    const old = document.querySelector(sel);
    if (!old) return;
    reopen(next, openKeys(old));
    const focus = focusKey(old);
    old.replaceWith(next);
    restoreFocus(next, focus);
  }, document.querySelector(sel) || seen);
  if (next.isConnected) pulse(kind === "card" ? next : next.querySelector(".headline") || next);
}

function onProject(data) {
  if (!data.slug) return;
  swap("card", data.slug, data.hash);
  swap("panels", data.slug, data.hash);
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
    if (!r.ok) return;
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
  source.addEventListener("open", () => {
    settle();
    setStatus(failing ? "warn" : "ok", failing ? failWord : "Live");
    if (missed) {
      missed = false;
      catchUp();
    }
  });
  source.addEventListener("error", () => {
    lost();
    // CONNECTING: the browser is retrying by itself.
    if (source.readyState !== EventSource.CLOSED) return;
    source.close();
    if (es === source) es = null;
    retryWait = retryWait ? Math.min(retryWait * 2, RETRY_MAX) : RETRY_FIRST;
    retryTimer = setTimeout(connect, Math.round(retryWait * (0.8 + Math.random() * 0.4)));
  });
  source.addEventListener("project", (e) => { const d = parse(e); if (d) onProject(d); });
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

document.addEventListener("waymark:toast", (e) => toast(e.detail || {}));
