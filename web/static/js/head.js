// The sticky header's real height, for app.css's scroll-padding: a link to
// an in-page target (#token, #access, a refused field) lands below the
// header even where the header wraps to more lines than --head-h allows
// for. The browser may have scrolled to the target before this ran, so on a
// fresh visit it scrolls there once more; on a reload or Back the browser
// puts the reader back where they were, and that stays. Without JavaScript
// --head-h stands.
const head = document.querySelector(".site-head");
if (head && "ResizeObserver" in window) {
  const root = document.documentElement;
  new ResizeObserver(() => root.style.setProperty("--head-seen", head.offsetHeight + "px")).observe(head);
  root.style.setProperty("--head-seen", head.offsetHeight + "px");
  // A malformed escape in the address must not stop app.js, which imports
  // this module: such a hash names no target.
  let id = "";
  try { id = decodeURIComponent(location.hash.slice(1)); } catch {}
  const fresh = performance.getEntriesByType("navigation")[0]?.type === "navigate";
  if (id && fresh) document.getElementById(id)?.scrollIntoView();
}
