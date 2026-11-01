// Heatmap: one tab stop, arrow keys move by day (up/down) and week
// (left/right); hover or focus shows the day in the readout line.
const wrap = document.querySelector("[data-heat]");
const grid = wrap?.querySelector(".heat");
const readout = document.querySelector("[data-heat-readout]");

if (grid) {
  const cells = [...grid.children];
  const days = wrap.querySelector(".heat-days");
  const real = (i) => cells[i] && !cells[i].classList.contains("pad");
  const show = (cell) => {
    if (readout && cell?.getAttribute("aria-label")) readout.textContent = cell.getAttribute("aria-label");
  };
  // Focus scrolling counts a cell half under the sticky weekday column as in
  // view, so nudge it clear.
  const keepClear = (cell) => {
    const edge = days.getBoundingClientRect().right + 3;
    const left = cell.getBoundingClientRect().left;
    if (left < edge) wrap.scrollLeft -= edge - left;
  };
  const move = (from, to) => {
    if (!real(to)) return;
    cells[from]?.setAttribute("tabindex", "-1");
    cells[to].setAttribute("tabindex", "0");
    cells[to].focus();
    keepClear(cells[to]);
  };
  grid.addEventListener("keydown", (e) => {
    const i = cells.indexOf(document.activeElement);
    if (i < 0) return;
    const step = { ArrowUp: -1, ArrowDown: 1, ArrowLeft: -7, ArrowRight: 7 }[e.key];
    if (step) {
      e.preventDefault();
      move(i, i + step);
    } else if (e.key === "Home" || e.key === "End") {
      e.preventDefault();
      let j = e.key === "Home" ? 0 : cells.length - 1;
      while (!real(j)) j += e.key === "Home" ? 1 : -1;
      move(i, j);
    }
  });
  grid.addEventListener("focusin", (e) => show(e.target));
  grid.addEventListener("pointerover", (e) => show(e.target.closest(".hc")));

  // A month name sliding under the sticky weekday column would show only its
  // tail ("y" of May), so hide it until it is wholly in view.
  const months = [...wrap.querySelectorAll(".heat-months span")].filter((s) => s.textContent);
  let queued = false;
  const hideCut = () => {
    queued = false;
    const edge = days.getBoundingClientRect().right;
    for (const m of months) m.classList.toggle("cut", m.getBoundingClientRect().left < edge - 1);
  };
  const queue = () => {
    if (!queued) { queued = true; requestAnimationFrame(hideCut); }
  };
  wrap.addEventListener("scroll", queue, { passive: true });
  window.addEventListener("resize", queue);

  // Newest weeks are on the right: start a narrow screen there.
  wrap.scrollLeft = wrap.scrollWidth;
  hideCut();
}
