// Entry module on every page. Everything here is an enhancement: the pages
// read the same without it.
import "./theme.js";
import "./time.js";
import "./palette.js";
import { whenActive } from "./util.js";

document.documentElement.classList.add("js");
for (const n of document.querySelectorAll("[data-js-show]")) n.hidden = false;

whenActive(() => {
  import("./live.js");
  if (document.querySelector("[data-since]")) import("./since.js");
});
if (document.querySelector("[data-ideas]")) import("./ideas.js");
if (document.querySelector("[data-heat]")) import("./heat.js");
if (document.getElementById("graph")) import("./map.js");
