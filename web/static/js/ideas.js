// Ideas page: the project select (JS only; the kind, size and issue filters
// are pure CSS), the empty state for the select, "Surprise me", and the
// filters in the address (?kind=, ?size=, ?filed=, ?project=), so a filtered
// view can be shared. The server pre-checks them from the link, so a shared
// link works without JS too.
import { el, icon, motionOK } from "./util.js";

const root = document.querySelector("[data-ideas]");
const cards = [...document.querySelectorAll(".idea-grid .idea")];
const select = document.getElementById("idea-project");
const count = document.querySelector("[data-idea-count]");
const surpriseBtn = document.querySelector("[data-surprise]");
const empty = document.querySelector("[data-ideas-empty]");
const FILTERS = ["kind", "size", "filed"];

const visible = (n) => (n.checkVisibility ? n.checkVisibility() : n.offsetParent !== null);

function updateCount() {
  const n = cards.filter(visible).length;
  if (count) count.textContent = n === cards.length ? "" : "Showing " + n + " of " + cards.length;
  // The CSS shows the message for an empty kind and size pair; this covers
  // the project select too.
  empty?.classList.toggle("show", n === 0);
  if (surpriseBtn) surpriseBtn.disabled = n === 0;
}

function applyProject() {
  const v = select ? select.value : "";
  for (const c of cards) c.hidden = v !== "" && c.dataset.project !== v;
}

// The address says what the filters show. replaceState, so Back still
// leaves the page.
function syncURL() {
  const q = new URLSearchParams();
  for (const name of FILTERS) {
    const v = root.querySelector(`input[name="${name}"]:checked`)?.value;
    if (v) q.set(name, v);
  }
  if (select?.value) q.set("project", select.value);
  const s = q.toString();
  history.replaceState(null, "", location.pathname + (s ? "?" + s : "") + location.hash);
}

function showAll() {
  for (const id of ["k-all", "s-all", "f-all"]) {
    const r = document.getElementById(id);
    if (r) r.checked = true;
  }
  if (select) select.value = "";
  for (const c of cards) c.hidden = false;
  updateCount();
  syncURL();
}

// The surprise pick keeps its outline and label until the next pick, Esc or
// a filter change, so it is easy to find again after the scroll.
let picked = null;

function clearPick() {
  if (!picked) return;
  picked.classList.remove("picked", "surprise");
  picked.querySelector(".picked-tag")?.remove();
  picked = null;
}

function surprise() {
  const pool = cards.filter(visible);
  if (!pool.length) return;
  // Never the same card twice in a row when there is another to pick.
  const choices = pool.length > 1 ? pool.filter((c) => c !== picked) : pool;
  const pick = choices[Math.floor(Math.random() * choices.length)];
  clearPick();
  picked = pick;
  pick.classList.add("picked");
  pick.prepend(el("span", { class: "picked-tag" }, icon("shuffle"), "Surprise pick"));
  pick.scrollIntoView({ behavior: motionOK() ? "smooth" : "auto", block: "center" });
  pick.focus({ preventScroll: true });
  if (motionOK()) {
    void pick.offsetWidth;
    pick.classList.add("surprise");
    pick.addEventListener("animationend", () => pick.classList.remove("surprise"), { once: true });
  }
}

if (root) {
  for (const n of root.querySelectorAll("[data-js-show]")) n.hidden = false;
  select?.addEventListener("change", () => {
    clearPick();
    applyProject();
    updateCount();
    syncURL();
  });
  root.addEventListener("change", (e) => {
    if (e.target instanceof HTMLInputElement && e.target.type === "radio") {
      clearPick();
      syncURL();
      requestAnimationFrame(updateCount);
    }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !e.defaultPrevented) clearPick();
  });
  // Without JS the link reloads /ideas; with it, reset in place.
  empty?.querySelector("a")?.addEventListener("click", (e) => {
    e.preventDefault();
    showAll();
    document.getElementById("k-all")?.focus();
  });
  surpriseBtn?.addEventListener("click", surprise);
  document.addEventListener("forgemap:surprise", surprise);
  // The project select may come pre-selected (a shared link) or restored (a
  // reload or Back), and so may the radios: apply the select, count once.
  applyProject();
  requestAnimationFrame(updateCount);
  if (location.hash === "#surprise") {
    history.replaceState(null, "", location.pathname + location.search);
    requestAnimationFrame(() => setTimeout(surprise, 150));
  }
}
