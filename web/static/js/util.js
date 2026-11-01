// Shared helpers. No data ever goes through innerHTML: text uses textContent
// and server fragments are parsed with Range.createContextualFragment.

const SVG = "http://www.w3.org/2000/svg";

// localStorage can throw (private windows, blocked storage); the page must
// read the same without it.
export const store = {
  get(key) {
    try { return window.localStorage.getItem(key); } catch { return null; }
  },
  set(key, value) {
    try { window.localStorage.setItem(key, value); } catch { /* ignore */ }
  },
};

const reduce = window.matchMedia("(prefers-reduced-motion: reduce)");
export const motionOK = () => !reduce.matches;

export function icon(name, cls) {
  const known = name && document.getElementById("i-" + name);
  const svg = document.createElementNS(SVG, "svg");
  svg.setAttribute("class", cls ? "i " + cls : "i");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  const use = document.createElementNS(SVG, "use");
  use.setAttribute("href", "#i-" + (known ? name : "circle-dashed"));
  svg.append(use);
  return svg;
}

// el("a", {href: "/x", class: "y"}, "text", child) builds DOM without HTML parsing.
export function el(tag, attrs, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v == null) continue;
    if (k === "text") node.textContent = v;
    else node.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children) {
    if (c == null || c === false) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
}

// Same wording as agoText in internal/web/helpers.go.
export function ago(when, now = Date.now()) {
  const t = typeof when === "number" ? when : Date.parse(when);
  if (!Number.isFinite(t)) return "never";
  const d = (now - t) / 1000;
  const plural = (n, one, many) => (n === 1 ? "1 " + one : n + " " + many);
  if (d < 10) return "just now";
  if (d < 60) return Math.floor(d) + " s ago";
  if (d < 3600) return plural(Math.floor(d / 60), "minute", "minutes") + " ago";
  if (d < 86400) return plural(Math.floor(d / 3600), "hour", "hours") + " ago";
  if (d < 172800) return "yesterday";
  const days = Math.floor(d / 86400);
  if (days < 60) return plural(days, "day", "days") + " ago";
  if (days < 730) return plural(Math.floor(days / 30), "month", "months") + " ago";
  return plural(Math.floor(days / 365), "year", "years") + " ago";
}

// Run fn once the page is really being shown (not while prerendering).
export function whenActive(fn) {
  if (document.prerendering) {
    document.addEventListener("prerenderingchange", () => fn(), { once: true });
  } else {
    fn();
  }
}

// Parse a server-rendered, server-escaped fragment into nodes.
export function fragment(html) {
  const range = document.createRange();
  range.selectNodeContents(document.body);
  return range.createContextualFragment(html);
}

// Run update inside a view transition when the browser has them, motion is
// allowed and the target is on screen; otherwise just run it.
export function transition(update, target) {
  const r = target?.getBoundingClientRect?.();
  const onScreen = !r || (r.bottom > 0 && r.top < window.innerHeight);
  if (document.startViewTransition && motionOK() && !document.hidden && onScreen) {
    return document.startViewTransition(update).finished.catch(() => {});
  }
  update();
  return Promise.resolve();
}

export function pulse(node) {
  if (!node || !motionOK()) return;
  node.classList.remove("pulse");
  void node.offsetWidth;
  node.classList.add("pulse");
  node.addEventListener("animationend", () => node.classList.remove("pulse"), { once: true });
}

export const isTyping = (target) =>
  target instanceof Element &&
  (target.closest("input, textarea, select, [contenteditable=''], [contenteditable='true']") !== null);
