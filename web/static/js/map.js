// Map: a small force simulation over the server's deterministic layout.
// Charge repulsion, link springs, a gentle pull to each category anchor and
// collision by radius. Nodes can be dragged; it settles and stops.
import { motionOK } from "./util.js";

const island = document.getElementById("graph");
const svg = document.getElementById("map-svg");
const scroller = document.querySelector("[data-map]");

const KIND = { "depends-on": "depends on", "client-of": "client of", "fork-of": "fork of", deploys: "deploys", sibling: "sibling of", monitors: "monitors", supersedes: "supersedes" };
const SVGNS = "http://www.w3.org/2000/svg";

let data = null;
try { data = JSON.parse(island?.textContent || "null"); } catch { data = null; }

if (data && svg) {
  const W = data.w;
  const H = data.h;
  const nodeEls = new Map([...svg.querySelectorAll(".node")].map((n) => [n.dataset.id, n]));
  const edgeEls = [...svg.querySelectorAll(".edge")];
  const labelLayer = svg.querySelector(".edge-labels");
  const anchors = new Map(data.anchors.map((a) => [a.key, a]));
  const catLabels = new Map([...svg.querySelectorAll(".cat-label")].map((t) => [t.dataset.cat, t]));
  const nodes = data.nodes.map((n) => {
    const el = nodeEls.get(n.id);
    let lw = 0;
    try { lw = el?.querySelector(".node-label")?.getComputedTextLength() || 0; } catch { lw = 0; }
    // Each star owns a box: the circle plus its label underneath.
    return { ...n, vx: 0, vy: 0, fixed: false, el, hw: Math.max(n.r, lw / 2) + 8, hh: n.r + 15, oy: 8 };
  });
  // Category names are boxes held near their anchors; stars orbit around them.
  const cats = data.anchors.map((a) => {
    let w = 0;
    const el = catLabels.get(a.key);
    try { w = el?.getComputedTextLength() || 0; } catch { w = 0; }
    return { x: a.x, y: a.y, ax: a.x, ay: a.y, fixed: false, cat: true, el, hw: w / 2 + 12, hh: 16, oy: -7 };
  });
  const bodies = nodes.concat(cats);
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const links = data.edges
    .map((e, i) => ({ s: byId.get(e.from), t: byId.get(e.to), kind: e.kind, el: edgeEls[i], line: edgeEls[i]?.querySelector("line") }))
    .filter((l) => l.s && l.t && l.el);
  const neighbours = new Map(nodes.map((n) => [n.id, new Set()]));
  for (const l of links) {
    neighbours.get(l.s.id).add(l.t.id);
    neighbours.get(l.t.id).add(l.s.id);
  }

  let alpha = 1;
  let running = false;
  const ALPHA_MIN = 0.01;

  // Box overlap, resolved along the shorter axis with a little sideways slide,
  // so a star wedged in a column of boxes can slip out instead of jamming.
  function collide() {
    const n = bodies.length;
    for (let i = 0; i < n; i++) {
      const a = bodies[i];
      for (let j = i + 1; j < n; j++) {
        const b = bodies[j];
        if (a.fixed && b.fixed) continue;
        const dx = b.x - a.x;
        const dy = b.y + b.oy - (a.y + a.oy);
        const ox = a.hw + b.hw - Math.abs(dx);
        const oy = a.hh + b.hh - Math.abs(dy);
        if (ox <= 0 || oy <= 0) continue;
        const wa = a.fixed ? 0 : b.fixed ? 1 : 0.5;
        const wb = 1 - wa;
        const sx = dx < 0 ? -1 : dx > 0 ? 1 : (i & 1 ? 1 : -1);
        const sy = dy < 0 ? -1 : dy > 0 ? 1 : (j & 1 ? 1 : -1);
        if (ox < oy) {
          a.x -= sx * ox * wa; b.x += sx * ox * wb;
          a.y -= sy * ox * 0.25 * wa; b.y += sy * ox * 0.25 * wb;
        } else {
          a.y -= sy * oy * wa; b.y += sy * oy * wb;
          a.x -= sx * oy * 0.25 * wa; b.x += sx * oy * 0.25 * wb;
        }
      }
    }
  }

  function boundAll() {
    for (const a of nodes) bound(a);
    for (const c of cats) {
      c.x = Math.min(Math.max(c.x, c.hw + 8), W - c.hw - 8);
      c.y = Math.min(Math.max(c.y, 30), H - 20);
    }
  }

  function bound(a) {
    a.x = Math.min(Math.max(a.x, a.hw + 12), W - a.hw - 12);
    a.y = Math.min(Math.max(a.y, a.r + 34), H - a.r - 34);
  }

  function tick() {
    const n = nodes.length;
    // Charge: every pair repels, weaker with distance.
    for (let i = 0; i < n; i++) {
      const a = nodes[i];
      for (let j = i + 1; j < n; j++) {
        const b = nodes[j];
        let dx = b.x - a.x;
        let dy = b.y - a.y;
        let d2 = dx * dx + dy * dy;
        if (d2 < 1) { dx = 1; dy = 0; d2 = 1; }
        if (d2 > 90000) continue;
        const d = Math.sqrt(d2);
        const f = (2600 * alpha) / Math.max(d2, 900);
        const fx = (dx / d) * f;
        const fy = (dy / d) * f;
        a.vx -= fx; a.vy -= fy;
        b.vx += fx; b.vy += fy;
      }
    }
    // Springs along relations: weak, so categories stay together.
    for (const l of links) {
      const dx = l.t.x - l.s.x;
      const dy = l.t.y - l.s.y;
      const d = Math.hypot(dx, dy) || 1;
      const f = ((d - 170) / d) * 0.006 * alpha;
      l.s.vx += dx * f; l.s.vy += dy * f;
      l.t.vx -= dx * f; l.t.vy -= dy * f;
    }
    // Category names spring back to their anchors.
    for (const c of cats) {
      c.x += (c.ax - c.x) * 0.3;
      c.y += (c.ay - c.y) * 0.3;
    }
    // Pull towards the category anchor.
    for (const a of nodes) {
      const an = anchors.get(a.cat);
      if (!an) continue;
      a.vx += (an.x - a.x) * 0.045 * alpha;
      a.vy += (an.y - a.y) * 0.045 * alpha;
    }
    for (const a of nodes) {
      if (a.fixed) { a.vx = 0; a.vy = 0; continue; }
      a.vx *= 0.55;
      a.vy *= 0.55;
      a.x += a.vx;
      a.y += a.vy;
    }
    for (let k = 0; k < 3; k++) collide();
    boundAll();
    alpha *= 0.985;
  }

  function draw() {
    for (const a of nodes) a.el?.setAttribute("transform", `translate(${a.x.toFixed(1)} ${a.y.toFixed(1)})`);
    for (const l of links) {
      l.line.setAttribute("x1", l.s.x.toFixed(1));
      l.line.setAttribute("y1", l.s.y.toFixed(1));
      l.line.setAttribute("x2", l.t.x.toFixed(1));
      l.line.setAttribute("y2", l.t.y.toFixed(1));
    }
    for (const c of cats) {
      c.el?.setAttribute("x", c.x.toFixed(1));
      c.el?.setAttribute("y", c.y.toFixed(1));
    }
    if (current) placeLabels();
  }

  // Once cool, finish with collision alone so no two labels overlap at rest.
  function settle() {
    for (let k = 0; k < 200; k++) {
      collide();
      boundAll();
    }
    svg.dataset.settled = "1";
  }

  function frame() {
    if (document.hidden) { running = false; return; }
    tick();
    if (alpha <= ALPHA_MIN) {
      settle();
      running = false;
    } else {
      requestAnimationFrame(frame);
    }
    draw();
  }

  function heat(to) {
    alpha = Math.max(alpha, to);
    if (!motionOK()) {
      while (alpha > ALPHA_MIN) tick();
      settle();
      draw();
      return;
    }
    if (!running) {
      running = true;
      requestAnimationFrame(frame);
    }
  }

  // Highlight a node, its relations and neighbours; dim the rest.
  let current = null;
  function placeLabels() {
    labelLayer.replaceChildren();
    if (!current) return;
    // One label per pair, even when two relations join the same stars.
    const pairs = new Map();
    for (const l of links) {
      if (l.s !== current && l.t !== current) continue;
      const key = [l.s.id, l.t.id].sort().join(" ");
      const p = pairs.get(key) || { l, words: [] };
      const w = KIND[l.kind] || l.kind;
      if (!p.words.includes(w)) p.words.push(w);
      pairs.set(key, p);
    }
    for (const { l, words } of pairs.values()) {
      const t = document.createElementNS(SVGNS, "text");
      t.setAttribute("class", "edge-label");
      t.setAttribute("x", ((l.s.x + l.t.x) / 2).toFixed(1));
      t.setAttribute("y", ((l.s.y + l.t.y) / 2 - 6).toFixed(1));
      t.textContent = words.join(", ");
      labelLayer.append(t);
    }
  }
  function focusNode(node) {
    current = node;
    svg.classList.toggle("focusing", !!node);
    const near = node ? neighbours.get(node.id) : null;
    for (const a of nodes) a.el?.classList.toggle("hl", !!node && (a === node || near.has(a.id)));
    for (const l of links) l.el.classList.toggle("hl", !!node && (l.s === node || l.t === node));
    placeLabels();
  }

  for (const a of nodes) {
    const el = a.el;
    if (!el) continue;
    el.addEventListener("pointerenter", () => focusNode(a));
    el.addEventListener("pointerleave", () => { if (!drag) focusNode(null); });
    el.addEventListener("focus", () => focusNode(a));
    el.addEventListener("blur", () => focusNode(null));
  }

  // Dragging. A press that moves less than 5 px is a click and navigates.
  let drag = null;
  let suppressUntil = 0;
  svg.addEventListener("click", (e) => {
    if (performance.now() < suppressUntil) {
      e.preventDefault();
      e.stopPropagation();
    }
  }, true);
  const toSvg = (e) => {
    const m = svg.getScreenCTM();
    if (!m) return { x: 0, y: 0 };
    const p = new DOMPoint(e.clientX, e.clientY).matrixTransform(m.inverse());
    return { x: p.x, y: p.y };
  };
  svg.addEventListener("pointerdown", (e) => {
    const el = e.target instanceof Element && e.target.closest(".node");
    if (!el || e.button !== 0) return;
    const a = byId.get(el.dataset.id);
    if (!a) return;
    const p = toSvg(e);
    drag = { a, el, dx: a.x - p.x, dy: a.y - p.y, sx: e.clientX, sy: e.clientY, moved: false, id: e.pointerId };
    el.setPointerCapture(e.pointerId);
  });
  svg.addEventListener("pointermove", (e) => {
    if (!drag || e.pointerId !== drag.id) return;
    if (!drag.moved && Math.hypot(e.clientX - drag.sx, e.clientY - drag.sy) < 5) return;
    if (!drag.moved) {
      drag.moved = true;
      drag.a.fixed = true;
      drag.el.classList.add("dragging");
    }
    const p = toSvg(e);
    drag.a.x = p.x + drag.dx;
    drag.a.y = p.y + drag.dy;
    bound(drag.a);
    heat(0.35);
    if (!running) draw();
  });
  const endDrag = (e) => {
    if (!drag || e.pointerId !== drag.id) return;
    const d = drag;
    drag = null;
    d.el.classList.remove("dragging");
    if (d.moved) {
      d.a.fixed = false;
      suppressUntil = performance.now() + 400;
      heat(0.25);
    }
  };
  svg.addEventListener("pointerup", endDrag);
  svg.addEventListener("pointercancel", endDrag);

  // Start on the middle of the map on narrow screens.
  if (scroller && scroller.scrollWidth > scroller.clientWidth) {
    scroller.scrollLeft = (scroller.scrollWidth - scroller.clientWidth) / 2;
  }
  heat(1);
}
