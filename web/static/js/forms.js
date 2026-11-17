// Help on top of the setup, sign-in and Settings forms, which work without
// it: a list of time zones and a guess from the browser, the private repos
// switch that needs sign-in, the address that GitHub does not need, a count
// of unsaved changes and a copy button. Each part does nothing when its hook
// is not on the page. The bare pages load this file themselves; the main
// layout's app.js loads it for a form[data-enhance].
import "./head.js";

// Time zones: the browser's list for the field, and its own zone in a field
// left empty or at UTC (what an unset zone reads as). Only then does the
// field's hint say the zone came from the browser.
const zones = document.getElementById("zones");
if (zones && typeof Intl.supportedValuesOf === "function") {
  for (const z of Intl.supportedValuesOf("timeZone")) zones.append(new Option(z));
}
for (const input of document.querySelectorAll("[data-zone-guess]")) {
  const guess = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (guess && (input.value.trim() === "" || input.value.trim() === "UTC")) {
    input.value = guess;
    document.getElementById("hint-" + input.name)?.append(" Taken from your browser.");
  }
}

// Private repos can only be shown while the page asks for sign-in: the
// switch is off and cannot be turned on while sign-in is off.
for (const box of document.querySelectorAll("[data-needs-signin]")) {
  const form = box.form;
  if (!form) continue;
  const sync = () => {
    const on = form.querySelector('[name="sign_in"]:checked')?.value === "on";
    box.disabled = !on;
    if (!on) box.checked = false;
  };
  form.addEventListener("change", sync);
  form.addEventListener("reset", () => setTimeout(sync));
  sync();
}

// GitHub has one address, so its field hides while GitHub is chosen. A
// hidden field is also disabled: a disabled control is neither checked nor
// sent, so an address typed before GitHub was chosen cannot stop Connect
// with a message nobody can see.
for (const form of document.querySelectorAll("form[data-forge-form]")) {
  const address = form.querySelector("[data-address]");
  const input = address?.querySelector('[name="address"]');
  if (!address || !input) continue;
  const sync = () => {
    address.hidden = form.querySelector('[name="type"]:checked')?.value === "github";
    input.disabled = address.hidden;
  };
  form.addEventListener("change", sync);
  form.addEventListener("reset", () => setTimeout(sync));
  sync();
}

// The save bar counts the settings changed and not yet saved.
for (const bar of document.querySelectorAll("[data-savebar]")) {
  const form = bar.closest("form");
  const out = bar.querySelector("[data-unsaved]");
  if (!form || !out) continue;
  const changed = (c) => {
    if (c.type === "checkbox" || c.type === "radio") return c.checked !== c.defaultChecked;
    if (c instanceof HTMLSelectElement) return [...c.options].some((o) => o.selected !== o.defaultSelected);
    return c.value !== c.defaultValue;
  };
  const count = () => {
    const names = new Set();
    for (const c of form.elements) {
      if (c.name && (c instanceof HTMLInputElement || c instanceof HTMLSelectElement || c instanceof HTMLTextAreaElement) && changed(c)) names.add(c.name);
    }
    const n = names.size;
    out.textContent = n === 0 ? "" : n === 1 ? "1 change not saved" : n + " changes not saved";
  };
  form.addEventListener("input", count);
  form.addEventListener("change", count);
  form.addEventListener("reset", () => setTimeout(count));
  count();
}

// Copy buttons put their address on the clipboard and say whether it worked.
for (const btn of document.querySelectorAll("[data-copy]")) {
  const word = btn.querySelector("span");
  if (!word) continue;
  const was = word.textContent;
  let timer = 0;
  btn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(btn.dataset.copy);
      word.textContent = "Copied";
    } catch {
      word.textContent = "Copy failed: select the address and copy it";
    }
    clearTimeout(timer);
    timer = setTimeout(() => { word.textContent = was; }, 3000);
  });
}

// The one-time sign-in link works once: a second press of Sign in, sent
// while the first is checked, would find the link spent and the wait still
// on, and the page shown would be that refusal. The form is sent once;
// coming back to the page lets it be sent again.
const onceForm = document.querySelector('input[name="once"]')?.form;
if (onceForm) {
  let sent = false;
  onceForm.addEventListener("submit", (e) => {
    if (sent) e.preventDefault();
    sent = true;
  });
  addEventListener("pageshow", () => { sent = false; });
}

// Setup's time left counts down each second from the seconds the server put
// in data-setup-left (never a clock time: the browser's clock may be wrong).
// The words change every second, but only the polite status line beside
// them is read out, at each whole minute and at 30 and 10 seconds. At zero
// the page loads /setup again, which says setup time has run out.
for (const line of document.querySelectorAll("[data-setup-left]")) {
  const words = line.querySelector("[data-left-words]");
  const said = document.querySelector("[data-left-said]");
  const start = Number(line.dataset.setupLeft);
  if (!words || !(start > 0)) continue;
  const unit = (n, one) => (n === 1 ? "1 " + one : n + " " + one + "s");
  const say = (s) => {
    const m = Math.floor(s / 60), sec = s % 60;
    if (m === 0) return unit(sec, "second");
    return sec === 0 ? unit(m, "minute") : unit(m, "minute") + " and " + unit(sec, "second");
  };
  const t0 = performance.now();
  const tick = () => {
    const left = start - Math.floor((performance.now() - t0) / 1000);
    if (left <= 0) {
      clearInterval(timer);
      location.assign("/setup");
      return;
    }
    words.textContent = say(left);
    if (said && (left % 60 === 0 || left === 30 || left === 10)) said.textContent = say(left) + " left to set up.";
  };
  const timer = setInterval(tick, 1000);
}
