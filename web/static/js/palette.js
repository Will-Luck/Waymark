// Command palette: Ctrl K, Cmd K or "/" (not while typing). Fuzzy subsequence
// search over projects, ideas, open issues and commands from /api/search.json.
import { el, icon, isTyping } from "./util.js";
import { toggleTheme } from "./theme.js";

const dialog = document.getElementById("palette");
const input = document.getElementById("pal-q");
const list = document.getElementById("pal-list");
const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

let index = null;
let loading = null;
let options = [];
let active = 0;
let opener = null;

for (const k of document.querySelectorAll("[data-kbd]")) k.textContent = isMac ? "⌘ K" : "Ctrl K";

function load() {
  if (index) return Promise.resolve(index);
  if (!loading) {
    loading = fetch("/api/search.json", { headers: { Accept: "application/json" } })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error("HTTP " + r.status))))
      .then((data) => (index = data))
      .catch((err) => {
        loading = null;
        throw err;
      });
  }
  return loading;
}

// Score a query against text: every query character must appear in order.
// Word starts, consecutive runs and an early first match score higher.
// Returns null for no match, else {score, hits} with the matched positions.
export function fuzzy(query, text) {
  if (!query) return { score: 0, hits: [] };
  const q = query.toLowerCase();
  const t = text.toLowerCase();
  let qi = 0;
  let score = 0;
  let last = -2;
  const hits = [];
  for (let i = 0; i < t.length && qi < q.length; i++) {
    if (t[i] !== q[qi]) continue;
    let s = 1;
    const prev = i === 0 ? " " : t[i - 1];
    if (i === 0) s += 8;
    else if (/[\s\-_/.(#:]/.test(prev)) s += 6;
    if (last === i - 1) s += 4;
    else if (last >= 0) s -= Math.min(3, (i - last) * 0.15);
    score += s;
    hits.push(i);
    last = i;
    qi++;
  }
  if (qi < q.length) return null;
  if (t.startsWith(q)) score += 10;
  score -= t.length * 0.02;
  return { score, hits };
}

function marked(text, hits) {
  const frag = document.createDocumentFragment();
  if (!hits?.length) {
    frag.append(text);
    return frag;
  }
  const set = new Set(hits);
  let run = "";
  let inHit = false;
  const flush = () => {
    if (!run) return;
    frag.append(inHit ? el("mark", null, run) : document.createTextNode(run));
    run = "";
  };
  for (let i = 0; i < text.length; i++) {
    const h = set.has(i);
    if (h !== inHit) {
      flush();
      inHit = h;
    }
    run += text[i];
  }
  flush();
  return frag;
}

function best(query, fields) {
  let top = null;
  fields.forEach((f, i) => {
    if (!f) return;
    const m = fuzzy(query, f);
    if (!m) return;
    const s = m.score - i * 4;
    if (!top || s > top.score) top = { score: s, hits: i === 0 ? m.hits : [], field: i };
  });
  return top;
}

function results(query) {
  const groups = [];
  const q = query.trim();
  const add = (label, items, limit) => {
    items.sort((a, b) => b.score - a.score);
    if (items.length) groups.push({ label, items: items.slice(0, limit) });
  };
  const projects = [];
  for (const p of index.projects) {
    let m = q ? best(q, [p.name, p.slug]) : { score: 0, hits: [] };
    // A summary only counts as a plain substring: subsequences of long prose
    // match almost anything.
    if (!m && q.length > 2 && p.summary.toLowerCase().includes(q.toLowerCase())) m = { score: 1, hits: [] };
    if (m) projects.push({ ...m, kind: "project", title: p.name, sub: p.summary, hint: p.tier_label, icon: p.icon, url: p.url, tile: true, cat: p.cat });
  }
  const commands = [];
  for (const c of index.commands) {
    const m = q ? best(q, [c.label]) : { score: 0, hits: [] };
    if (m) commands.push({ ...m, kind: "command", title: c.label, icon: c.icon, url: c.url, action: c.action });
  }
  if (!q) {
    add("Go to", commands, 20);
    add("Projects", projects, 8);
    return groups;
  }
  const ideas = [];
  for (const i of index.ideas) {
    const m = best(q, [i.title, i.project_name]);
    if (m) ideas.push({ ...m, kind: "idea", title: i.title, sub: i.project_name, icon: i.icon, url: i.url });
  }
  const issues = [];
  for (const i of index.issues) {
    const label = "#" + i.number + " " + i.title;
    const m = best(q, [label, i.project_name]);
    if (m) issues.push({ ...m, kind: "issue", title: label, sub: i.project_name + (i.forge ? ", " + i.forge : ""), icon: i.mirror ? "github" : "circle-dot", url: i.url });
  }
  add("Projects", projects, 7);
  add("Commands", commands, 7);
  add("Ideas", ideas, 6);
  add("Issues", issues, 6);
  // The group holding the strongest match comes first, so Enter takes it.
  return groups.sort((a, b) => b.items[0].score - a.items[0].score);
}

function render() {
  const groups = results(input.value);
  list.replaceChildren();
  options = [];
  if (!groups.length) {
    list.append(el("li", { class: "pal-empty", role: "presentation" }, "Nothing matches. Try fewer letters."));
    input.removeAttribute("aria-activedescendant");
    return;
  }
  for (const g of groups) {
    list.append(el("li", { class: "pal-group", role: "presentation" }, g.label));
    for (const it of g.items) {
      const id = "pal-o-" + options.length;
      const lead = it.tile ? el("span", { class: "tile", "data-cat": it.cat || null }, icon(it.icon)) : el("span", { class: "pal-ic" }, icon(it.icon));
      const opt = el(
        "li",
        { id, class: "pal-opt", role: "option", "aria-selected": "false" },
        lead,
        el("span", { class: "pal-title" }, marked(it.title, it.hits)),
        it.sub ? el("span", { class: "pal-sub" }, it.sub) : null,
        it.hint ? el("span", { class: "pal-hint tp" }, it.hint) : null,
      );
      opt.addEventListener("pointermove", () => select(options.indexOf(it)));
      opt.addEventListener("click", () => choose(it));
      it.node = opt;
      options.push(it);
      list.append(opt);
    }
  }
  select(0);
}

function select(i) {
  if (!options.length) return;
  active = (i + options.length) % options.length;
  options.forEach((o, n) => o.node.setAttribute("aria-selected", n === active ? "true" : "false"));
  const node = options[active].node;
  input.setAttribute("aria-activedescendant", node.id);
  node.scrollIntoView({ block: "nearest" });
}

async function choose(it) {
  if (!it) return;
  if (it.action === "theme") {
    close();
    toggleTheme(document.querySelector("[data-theme-toggle]"));
    return;
  }
  if (it.action === "refresh") {
    close();
    try {
      const r = await fetch("/api/refresh", { method: "POST" });
      document.dispatchEvent(new CustomEvent("waymark:toast", { detail: { text: r.ok ? "Refresh asked for. New data arrives live." : "Refresh failed (HTTP " + r.status + ").", level: r.ok ? "good" : "warn", icon: "refresh-cw" } }));
    } catch {
      document.dispatchEvent(new CustomEvent("waymark:toast", { detail: { text: "Refresh failed: no connection.", level: "warn", icon: "refresh-cw" } }));
    }
    return;
  }
  if (it.action === "surprise") {
    close();
    if (document.querySelector("[data-ideas]")) document.dispatchEvent(new CustomEvent("waymark:surprise"));
    else location.href = "/ideas#surprise";
    return;
  }
  if (it.url) {
    close();
    location.href = it.url;
  }
}

export async function open(from) {
  if (!dialog || dialog.open) return;
  opener = from || document.activeElement;
  input.value = "";
  list.replaceChildren(el("li", { class: "pal-empty", role: "presentation" }, "Loading"));
  dialog.showModal();
  input.focus();
  try {
    await load();
    render();
  } catch {
    list.replaceChildren(el("li", { class: "pal-empty", role: "presentation" }, "Search is unavailable right now."));
  }
}

function close() {
  if (dialog.open) dialog.close();
}

if (dialog) {
  input.addEventListener("input", () => index && render());
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); select(active + 1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); select(active - 1); }
    else if (e.key === "Home" && e.ctrlKey) { e.preventDefault(); select(0); }
    else if (e.key === "End" && e.ctrlKey) { e.preventDefault(); select(options.length - 1); }
    else if (e.key === "Enter") { e.preventDefault(); choose(options[active]); }
  });
  dialog.addEventListener("click", (e) => {
    if (e.target === dialog) close();
  });
  dialog.addEventListener("close", () => {
    if (opener && opener.isConnected && typeof opener.focus === "function") opener.focus();
    opener = null;
  });
  document.addEventListener("keydown", (e) => {
    const k = e.key.toLowerCase();
    if ((e.ctrlKey || e.metaKey) && k === "k") {
      e.preventDefault();
      if (dialog.open) close();
      else open();
      return;
    }
    if (e.key === "/" && !e.ctrlKey && !e.metaKey && !e.altKey && !isTyping(e.target) && !dialog.open) {
      e.preventDefault();
      open();
    }
  });
  document.addEventListener("click", (e) => {
    const b = e.target instanceof Element && e.target.closest("[data-palette-open]");
    if (b) open(b);
  });
  // Warm the index on first intent so the palette opens instantly.
  document.querySelector(".search-btn")?.addEventListener("pointerenter", () => load().catch(() => {}), { once: true });
}
