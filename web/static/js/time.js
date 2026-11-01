// Keeps every server-rendered <time data-ago> current: the header every 5 s,
// everything else every 30 s.
import { ago } from "./util.js";

export function refreshTimes(selector = "time[data-ago]") {
  const now = Date.now();
  for (const t of document.querySelectorAll(selector)) {
    const text = ago(t.getAttribute("datetime"), now);
    if (t.textContent !== text) t.textContent = text;
  }
}

refreshTimes();
setInterval(() => refreshTimes("time[data-ago-fast]"), 5000);
setInterval(() => refreshTimes(), 30000);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refreshTimes();
});
