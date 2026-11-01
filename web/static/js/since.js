// "Since you last looked": the server renders 14 days of events with data-at
// and hides those older than 7 days. Here the viewer's own last visit decides.
// A visit ends at pagehide; a return within 30 minutes counts as the same
// visit, so opening a project and coming back does not wipe the list.
import { store, ago } from "./util.js";

const section = document.querySelector("[data-since]");
const SAME_VISIT = 30 * 60 * 1000;
const CAP = 12;

if (section) {
  const now = Date.now();
  const lastSeen = Number(store.get("lastVisit")) || 0;
  let baseline = Number(store.get("lastVisitBaseline")) || 0;
  if (lastSeen && now - lastSeen > SAME_VISIT) baseline = lastSeen;
  if (baseline) store.set("lastVisitBaseline", String(baseline));

  const items = [...section.querySelectorAll(".ev[data-at]")];
  const sum = section.querySelector("[data-since-sum]");
  const all = section.querySelector("[data-since-all]");

  if (baseline) {
    let shown = 0;
    const fresh = [];
    for (const li of items) {
      li.removeAttribute("data-old");
      const isNew = Number(li.dataset.at) > baseline;
      li.hidden = !isNew;
      if (isNew) fresh.push(li);
    }
    for (const li of fresh) {
      shown++;
      if (shown > CAP) li.hidden = true;
    }
    if (sum) {
      sum.replaceChildren();
      const n = fresh.length;
      const b = document.createElement("b");
      b.textContent = n === 1 ? "1 change" : n + " changes";
      sum.append(b, " since you were last here, " + ago(baseline) + ".");
    }
    if (all && fresh.length > CAP) {
      all.hidden = false;
      all.lastChild.textContent = "Show all " + fresh.length + " changes";
      all.addEventListener("click", () => {
        for (const li of fresh) li.hidden = false;
        all.hidden = true;
      }, { once: true });
    }
  }

  window.addEventListener("pagehide", () => store.set("lastVisit", String(Date.now())));
}
