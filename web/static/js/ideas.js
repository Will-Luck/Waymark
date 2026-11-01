// Ideas page: the project select (JS only; kind and size filters are pure
// CSS), the empty state for the select, and "Surprise me".
import { motionOK } from "./util.js";

const root = document.querySelector("[data-ideas]");
const cards = [...document.querySelectorAll(".idea-grid .idea")];
const select = document.getElementById("idea-project");
const count = document.querySelector("[data-idea-count]");
const surpriseBtn = document.querySelector("[data-surprise]");
const empty = document.querySelector("[data-ideas-empty]");

const visible = (n) => (n.checkVisibility ? n.checkVisibility() : n.offsetParent !== null);

function updateCount() {
  const n = cards.filter(visible).length;
  if (count) count.textContent = n === cards.length ? "" : "Showing " + n + " of " + cards.length;
  // The CSS shows the message for an empty kind and size pair; this covers
  // the project select too.
  empty?.classList.toggle("show", n === 0);
  if (surpriseBtn) surpriseBtn.disabled = n === 0;
}

function showAll() {
  for (const id of ["k-all", "s-all"]) {
    const r = document.getElementById(id);
    if (r) r.checked = true;
  }
  if (select) select.value = "";
  for (const c of cards) c.hidden = false;
  updateCount();
}

function surprise() {
  const pool = cards.filter(visible);
  if (!pool.length) return;
  const pick = pool[Math.floor(Math.random() * pool.length)];
  pick.scrollIntoView({ behavior: motionOK() ? "smooth" : "auto", block: "center" });
  pick.focus({ preventScroll: true });
  pick.classList.remove("surprise");
  if (motionOK()) {
    void pick.offsetWidth;
    pick.classList.add("surprise");
    pick.addEventListener("animationend", () => pick.classList.remove("surprise"), { once: true });
  }
}

if (root) {
  for (const n of root.querySelectorAll("[data-js-show]")) n.hidden = false;
  select?.addEventListener("change", () => {
    const v = select.value;
    for (const c of cards) c.hidden = v !== "" && c.dataset.project !== v;
    updateCount();
  });
  root.addEventListener("change", (e) => {
    if (e.target instanceof HTMLInputElement && e.target.type === "radio") requestAnimationFrame(updateCount);
  });
  // Without JS the link reloads /ideas; with it, reset in place.
  empty?.querySelector("a")?.addEventListener("click", (e) => {
    e.preventDefault();
    showAll();
    document.getElementById("k-all")?.focus();
  });
  surpriseBtn?.addEventListener("click", surprise);
  document.addEventListener("waymark:surprise", surprise);
  // A reload or Back can restore checked radios, so count once at start.
  requestAnimationFrame(updateCount);
  if (location.hash === "#surprise") {
    history.replaceState(null, "", location.pathname);
    requestAnimationFrame(() => setTimeout(surprise, 150));
  }
}
