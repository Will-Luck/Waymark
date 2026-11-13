// "Since you last looked". The server renders 14 days of events, each
// project's runs folded into rows (internal/web/since.go), and its first
// render shows the last 7 days: at most 3 rows per project and 12 in all,
// every project's first row before any project's second. Here the viewer's
// own last visit decides which events count; the rows are counted again and
// the same caps apply. A visit ends at pagehide; a return within 30 minutes
// counts as the same visit, so opening a project and coming back does not
// wipe the list.
import { store, ago } from "./util.js";

const section = document.querySelector("[data-since]");
const SAME_VISIT = 30 * 60 * 1000;
// The same numbers as sinceCap and sincePerProject in internal/web/since.go.
const CAP = 12;
const PER = 3;

const nums = (s) => (s || "").split(" ").filter(Boolean).map(Number);

// A folded row's words for its newest keep members, whose weights add up to
// n: the newest member's own words when it stands alone or nothing adds up.
// The tokens are filled in one pass, so braces in the subject (data-subj,
// words from outside) stay as written. The same rule as fill in
// internal/web/since.go.
function fill(li, keep, n) {
  if (keep <= 1 || n === 0) return li.dataset.single;
  const by = { n: String(n), times: n === 1 ? "once" : n + " times", subj: li.dataset.subj || "" };
  return li.dataset.fold.replace(/\{(n|times|subj)\}/g, (_, k) => by[k]);
}

// recount sets a row's words for its events after baseline and returns how
// many there are. A folded row lists its members newest first.
function recount(li, baseline) {
  if (!li.dataset.ats) return Number(li.dataset.at) > baseline ? 1 : 0;
  const ats = nums(li.dataset.ats);
  const keep = ats.filter((t) => t > baseline).length;
  if (keep === 0) return 0;
  const n = nums(li.dataset.ws).slice(0, keep).reduce((a, b) => a + b, 0);
  const words = li.querySelector("[data-ev-words]");
  if (words) words.textContent = fill(li, keep, n);
  return keep;
}

// Round by round, every group with another row gets one, up to PER rounds
// and CAP rows in all. The same rule as pickRows in internal/web/since.go.
function pick(counts) {
  const picks = counts.map(() => 0);
  let total = 0;
  for (let round = 0; round < PER; round++) {
    for (let i = 0; i < counts.length; i++) {
      if (total === CAP) return picks;
      if (counts[i] > round) {
        picks[i]++;
        total++;
      }
    }
  }
  return picks;
}

function relayout(baseline) {
  const main = section.querySelector("[data-since-main]");
  const rest = section.querySelector("[data-since-rest]");
  const restList = section.querySelector("[data-since-rest-list]");
  const restWord = section.querySelector("[data-since-rest-word]");
  const sum = section.querySelector("[data-since-sum]");
  if (!main || !rest || !restList) return;
  // Groups are in the server's order, newest change first, main then rest.
  const groups = [...section.querySelectorAll(".since-group")];
  let changes = 0;
  const fresh = groups.map((g) => {
    const rows = [];
    for (const li of g.querySelectorAll(".ev[data-at]")) {
      li.removeAttribute("data-old");
      const keep = recount(li, baseline);
      li.hidden = keep === 0;
      changes += keep;
      if (keep) rows.push(li);
    }
    return rows;
  });
  const picks = pick(fresh.map((rows) => rows.length));
  let restN = 0;
  groups.forEach((g, i) => {
    const rows = fresh[i];
    const n = picks[i] || Math.min(rows.length, PER);
    const top = g.querySelector("[data-since-top]");
    const more = g.querySelector("[data-since-more]");
    const extra = g.querySelector("[data-since-extra]");
    const word = g.querySelector("[data-since-more-word]");
    if (top && extra) {
      top.append(...rows.slice(0, n));
      extra.append(...rows.slice(n), ...g.querySelectorAll(".ev[hidden]"));
    }
    if (word) word.textContent = rows.length - n + " more";
    if (more) more.hidden = rows.length === n;
    if (picks[i]) {
      main.append(g);
    } else {
      restList.append(g);
      if (rows.length) restN++;
    }
  });
  rest.hidden = restN === 0;
  if (restWord) restWord.textContent = restN === 1 ? "1 more project" : restN + " more projects";
  if (sum) {
    sum.replaceChildren();
    const b = document.createElement("b");
    b.textContent = changes === 1 ? "1 change" : changes + " changes";
    sum.append(b, " since you were last here, " + ago(baseline) + ".");
  }
}

if (section) {
  const now = Date.now();
  const lastSeen = Number(store.get("lastVisit")) || 0;
  let baseline = Number(store.get("lastVisitBaseline")) || 0;
  if (lastSeen && now - lastSeen > SAME_VISIT) baseline = lastSeen;
  if (baseline) store.set("lastVisitBaseline", String(baseline));
  if (baseline) relayout(baseline);

  window.addEventListener("pagehide", () => store.set("lastVisit", String(Date.now())));
}
