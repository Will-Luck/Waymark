// Theme: follows the system until the toggle overrides it. The choice is kept
// in localStorage and mirrored in a cookie so the server can render the right
// data-theme on the next page (no flash, no blocking script).
import { store, motionOK } from "./util.js";

const root = document.documentElement;
const dark = window.matchMedia("(prefers-color-scheme: dark)");

export const effectiveTheme = () => root.dataset.theme || (dark.matches ? "dark" : "light");

function writeCookie(theme) {
  try {
    document.cookie = "theme=" + theme + "; path=/; max-age=31536000; samesite=lax";
  } catch { /* ignore */ }
}

function syncButtons() {
  const next = effectiveTheme() === "dark" ? "light" : "dark";
  for (const b of document.querySelectorAll("[data-theme-toggle]")) {
    b.setAttribute("aria-label", "Switch to " + next + " theme");
    b.title = "Switch to " + next + " theme";
  }
}

function apply(theme) {
  root.dataset.theme = theme;
  store.set("theme", theme);
  writeCookie(theme);
  syncButtons();
}

// A circular reveal from the button that was pressed, when allowed.
export function toggleTheme(origin) {
  const next = effectiveTheme() === "dark" ? "light" : "dark";
  if (!document.startViewTransition || !motionOK()) {
    apply(next);
    return;
  }
  const r = origin?.getBoundingClientRect?.();
  const x = r ? r.left + r.width / 2 : window.innerWidth - 40;
  const y = r ? r.top + r.height / 2 : 30;
  const far = Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y));
  root.style.setProperty("--vt-x", x + "px");
  root.style.setProperty("--vt-y", y + "px");
  root.style.setProperty("--vt-r", far + "px");
  root.classList.add("theme-vt");
  const vt = document.startViewTransition(() => apply(next));
  vt.finished.finally(() => root.classList.remove("theme-vt"));
}

const saved = store.get("theme");
if ((saved === "light" || saved === "dark") && root.dataset.theme !== saved) {
  root.dataset.theme = saved;
  writeCookie(saved);
}
syncButtons();
dark.addEventListener("change", syncButtons);
document.addEventListener("click", (e) => {
  const b = e.target instanceof Element && e.target.closest("[data-theme-toggle]");
  if (b) toggleTheme(b);
});
