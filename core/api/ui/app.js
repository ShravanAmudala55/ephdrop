"use strict";

// The desktop app's shell adds window.ephdropShell (see desktop/preload.js).
// Without it this page still works in a browser, just without dragging files
// out and without picking files by path.
const shell = window.ephdropShell || null;

const $ = (id) => document.getElementById(id);
let state = { self: {}, peers: [], items: [], invite: null };
const saved = new Map();   // file id -> path it was saved to
const saving = new Set();  // file ids being downloaded
const drag = new Map();    // file id -> {state: "preparing" | "ready" | "failed", path}
let filter = "all";        // "all", "local" or a device id
let selected = null;       // id of the selected file
let query = "";            // text in the search box

// ------------------------------------------------------------ helpers

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  if (res.status === 204) return null;
  let data = null;
  try { data = await res.json(); } catch (_) { /* no body */ }
  if (!res.ok) throw new Error((data && data.error) || `Request failed (${res.status})`);
  return data;
}

function toast(text, isError) {
  const el = document.createElement("div");
  el.className = "toast" + (isError ? " error" : "");
  el.textContent = text;
  $("toasts").appendChild(el);
  setTimeout(() => el.remove(), isError ? 6000 : 3500);
}

function friendly(err) {
  const m = String(err && err.message || err);
  if (/unreachable|cannot be reached/i.test(m)) return "That device is not reachable right now. Check it is on the same Wi-Fi with ephdrop open.";
  if (/not found|unknown file|expired/i.test(m)) return "That file has expired or was removed.";
  return m.charAt(0).toUpperCase() + m.slice(1);
}

function size(n) {
  if (n < 1000) return `${n} B`;
  const units = ["kB", "MB", "GB", "TB"];
  let i = -1;
  do { n /= 1000; i++; } while (n >= 1000 && i < units.length - 1);
  return `${n < 10 ? n.toFixed(1) : Math.round(n)} ${units[i]}`;
}

function timeLeft(ms) {
  if (ms <= 0) return "Expired";
  const s = Math.floor(ms / 1000), m = Math.floor(s / 60), h = Math.floor(m / 60), d = Math.floor(h / 24);
  if (d >= 2) return `${d} days left`;
  if (h >= 1) return `${h}h ${m % 60}m left`;
  if (m >= 1) return `${m} min left`;
  return "Less than a minute left";
}

function short(ms) {
  if (ms <= 0) return "Expired";
  const m = Math.floor(ms / 60000), h = Math.floor(m / 60), d = Math.floor(h / 24);
  if (d >= 1) return h % 24 ? `${d}d ${h % 24}h` : `${d}d`;
  if (h >= 1) return h >= 6 || m % 60 === 0 ? `${h}h` : `${h}h ${m % 60}m`;
  if (m >= 1) return `${m}m`;
  return "<1m";
}

function ext(name) {
  const i = name.lastIndexOf(".");
  if (i <= 0 || i === name.length - 1) return "file";
  return name.slice(i + 1, i + 5).toLowerCase();
}

function ttlHours() {
  return parseFloat($("ttl").value) || 24;
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// ------------------------------------------------------------ rendering

const SVGNS = "http://www.w3.org/2000/svg";

function render() {
  if (filter !== "all" && filter !== "local" && !state.peers.some((p) => p.id === filter)) filter = "all";
  renderNav();
  renderDevices();
  renderFiles();
}

// Each device keeps its colour for as long as it is paired.
function peerClass(p) {
  const i = state.peers.findIndex((x) => x.id === p.id);
  return "dev-" + (i < 0 ? 0 : i % 5);
}

function liveItems() {
  const now = Date.now();
  return state.items.filter((it) => Date.parse(it.expires) > now);
}

function navButton(label, cls, count, key) {
  const li = el("li");
  const b = el("button", cls);
  b.type = "button";
  b.append(el("span", "dot"), el("span", "label", label), el("span", "n", String(count)));
  if (filter === key) b.setAttribute("aria-current", "true");
  b.addEventListener("click", () => { filter = key; selected = null; render(); });
  li.append(b);
  return li;
}

function renderNav() {
  const items = liveItems();
  const ul = $("nav");
  ul.replaceChildren();
  const all = navButton("All files", "", items.length, "all");
  all.firstChild.firstChild.style.visibility = "hidden";
  const mine = navButton("This device", "dev-self", items.filter((i) => i.local).length, "local");
  if (state.self.name) mine.firstChild.title = state.self.name;
  ul.append(all, mine);
}

function renderDevices() {
  const ul = $("devices");
  const items = liveItems();
  ul.replaceChildren();
  for (const p of state.peers) {
    const li = el("li");
    const b = el("button", `chip ${peerClass(p)} ${p.online ? "online" : "offline"}`);
    b.type = "button";
    b.append(el("span", "dot"), el("span", "label", p.name), el("span", "n", String(items.filter((i) => i.holder === p.id).length)));
    b.title = p.online ? `${p.name} is online` : `${p.name} is not reachable right now`;
    if (filter === p.id) b.setAttribute("aria-current", "true");
    b.addEventListener("click", () => { filter = p.id; selected = null; render(); });
    const rm = el("button", "rm", "×");
    rm.type = "button";
    rm.title = `Remove ${p.name}`;
    rm.setAttribute("aria-label", `Remove ${p.name}`);
    rm.addEventListener("click", () => askRemove(p));
    li.append(b, rm);
    ul.append(li);
  }
  const add = el("li");
  const ab = el("button", "chip add", "+ Add device");
  ab.type = "button";
  ab.addEventListener("click", openPair);
  add.append(ab);
  ul.append(add);
}

function visibleItems() {
  const q = query.trim().toLowerCase();
  return liveItems().filter((it) => {
    if (filter === "local" && !it.local) return false;
    if (filter !== "all" && filter !== "local" && it.holder !== filter) return false;
    if (q && !(it.name.toLowerCase().includes(q) || String(it.holderName || "").toLowerCase().includes(q))) return false;
    return true;
  });
}

function renderFiles() {
  const body = $("files");
  const empty = $("empty");
  const now = Date.now();
  const items = visibleItems();
  body.replaceChildren();
  for (const it of items) body.append(fileRow(it, now));
  $("table").hidden = items.length === 0;
  if (selected && !items.some((i) => i.id === selected)) selected = null;

  empty.replaceChildren();
  empty.hidden = items.length > 0;
  if (items.length === 0) {
    if (query.trim()) {
      empty.append(el("h2", "", "No matches"), el("p", "", "No shared file has that name. Clear the search to see everything."));
    } else if (state.peers.length === 0) {
      empty.append(el("h2", "", "No devices yet"), el("p", "", "Add your phone or another computer, then drop files here to share them. Files disappear on their own after a day."));
      const b = el("button", "primary", "Add device");
      b.addEventListener("click", openPair);
      empty.append(b);
    } else {
      empty.append(el("h2", "", "Nothing shared"), el("p", "", "Drop files anywhere in this window. Your other devices can pick them up until they expire."));
    }
  }
  renderBar(items);
}

function ring(frac) {
  const C = 2 * Math.PI * 10;
  const svg = document.createElementNS(SVGNS, "svg");
  svg.setAttribute("viewBox", "0 0 26 26");
  svg.setAttribute("class", "ring");
  svg.setAttribute("aria-hidden", "true");
  const track = document.createElementNS(SVGNS, "circle");
  const arc = document.createElementNS(SVGNS, "circle");
  for (const c of [track, arc]) { c.setAttribute("cx", "13"); c.setAttribute("cy", "13"); c.setAttribute("r", "10"); }
  track.setAttribute("class", "track");
  arc.setAttribute("class", "arc");
  arc.setAttribute("stroke-dasharray", `${(C * frac).toFixed(2)} ${C.toFixed(2)}`);
  arc.setAttribute("transform", "rotate(-90 13 13)");
  svg.append(track, arc);
  return svg;
}

function fileRow(it, now) {
  const total = Math.max(1, Date.parse(it.expires) - Date.parse(it.created));
  const ms = Date.parse(it.expires) - now;
  const frac = Math.min(1, Math.max(0, ms / total));
  const d = drag.get(it.id);
  const dev = it.local ? "dev-self" : (() => { const p = state.peers.find((x) => x.id === it.holder); return p ? peerClass(p) : "dev-0"; })();

  const tr = el("tr", ["file", dev, ms < 3600e3 ? "low" : "", selected === it.id ? "sel" : "",
    !it.local && !it.reachable ? "gone" : "", d && d.state === "preparing" ? "preparing" : ""].filter(Boolean).join(" "));
  tr.dataset.id = it.id;
  tr.tabIndex = 0;

  const nm = el("td", "nm");
  nm.append(el("span", "x", ext(it.name)));
  const fname = el("span", "fname", it.name);
  fname.title = it.name;
  nm.append(fname);
  if (shell && d && d.state === "ready") nm.append(el("span", "ready", "Ready to drag out"));
  tr.append(nm);

  const from = el("td", "from");
  const who = el("span", "who");
  who.append(el("span", "dot"), document.createTextNode(" " + (it.local ? "This device" : (it.holderName || "Another device"))));
  from.append(who);
  if (!it.local && !it.reachable) from.append(el("span", "mute", " · offline"));
  tr.append(from);

  tr.append(el("td", "num", size(it.size)));

  const exp = el("td", "exp");
  exp.title = timeLeft(ms);
  exp.append(ring(frac), el("span", "tl", short(ms)));
  tr.append(exp);

  tr.addEventListener("click", () => select(it.id));
  tr.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); select(it.id); } });
  tr.addEventListener("dblclick", () => { if (!it.local && it.reachable && !saved.has(it.id) && !saving.has(it.id)) saveFile(it); });

  if (shell && (it.local || it.reachable)) {
    tr.classList.add("draggable");
    tr.draggable = true;
    tr.addEventListener("pointerenter", () => prepareDrag(it));
    tr.addEventListener("focusin", () => prepareDrag(it));
    tr.addEventListener("dragstart", (e) => onDragStart(e, it));
  }
  return tr;
}

function select(id) {
  selected = id;
  for (const r of $("files").children) r.classList.toggle("sel", r.dataset.id === id);
  renderBar(visibleItems());
}

function renderBar(items) {
  const bar = $("bar");
  bar.replaceChildren();
  const it = items.find((i) => i.id === selected);
  if (!it) {
    bar.append(el("span", "mute", items.length === 0 ? "" : shell ? "Select a file to save it, or drag it out of this window to copy it anywhere." : "Select a file to save it."));
    return;
  }
  const who = el("span", "who");
  who.append(el("b", "", it.name), el("span", "mute", it.local ? " on this device" : ` from ${it.holderName || "another device"}`));
  bar.append(who, el("span", "sp"));
  if (it.local) {
    if (!shell) {
      const a = el("a", "linkish", "Download");
      a.href = `/api/files/${encodeURIComponent(it.id)}`;
      bar.append(a);
    }
    const rm = el("button", "", "Remove");
    rm.type = "button";
    rm.addEventListener("click", () => removeFile(it));
    bar.append(rm);
  } else if (saved.has(it.id)) {
    bar.append(el("span", "mute", "Saved"));
    if (shell) {
      const show = el("button", "", "Show in folder");
      show.type = "button";
      show.addEventListener("click", () => shell.showInFolder(saved.get(it.id)));
      bar.append(show);
    }
  } else {
    if (!it.reachable) bar.append(el("span", "mute", "Not reachable right now"));
    const sv = el("button", "primary", saving.has(it.id) ? "Saving" : "Save");
    sv.type = "button";
    sv.disabled = !it.reachable || saving.has(it.id);
    sv.addEventListener("click", () => saveFile(it));
    bar.append(sv);
  }
}

// ------------------------------------------------------------ actions

async function removeFile(it) {
  try {
    await api("DELETE", `/api/files/${encodeURIComponent(it.id)}`);
    toast(`Removed ${it.name}`);
  } catch (e) { toast(friendly(e), true); }
}

async function saveFile(it) {
  saving.add(it.id);
  renderFiles();
  try {
    const r = await api("POST", "/api/fetch", { holder: it.holder, id: it.id });
    saved.set(it.id, r.path);
    toast(`Saved ${it.name}`);
  } catch (e) {
    toast(friendly(e), true);
  } finally {
    saving.delete(it.id);
    renderFiles();
  }
}

async function shareFiles(files) {
  let ok = 0;
  for (const f of files) {
    try {
      if (shell) {
        const path = shell.pathForFile(f);
        if (!path) throw new Error(`${f.name} is not a file on this computer`);
        await api("POST", "/api/share", { path, ttlHours: ttlHours() });
      } else {
        const res = await fetch(`/api/upload?name=${encodeURIComponent(f.name)}&ttlHours=${ttlHours()}`, { method: "PUT", body: f });
        if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || "Upload failed");
      }
      ok++;
    } catch (e) {
      toast(`${f.name}: ${friendly(e)}`, true);
    }
  }
  if (ok) toast(ok === 1 ? "Shared 1 file" : `Shared ${ok} files`);
}

async function addByPaths(paths) {
  let ok = 0;
  for (const p of paths) {
    try {
      await api("POST", "/api/share", { path: p, ttlHours: ttlHours() });
      ok++;
    } catch (e) { toast(friendly(e), true); }
  }
  if (ok) toast(ok === 1 ? "Shared 1 file" : `Shared ${ok} files`);
}

// ------------------------------------------------------------ dragging

async function prepareDrag(it) {
  const have = drag.get(it.id);
  if (have) return;
  if (it.size > shell.maxDragBytes) { drag.set(it.id, { state: "failed", path: "" }); return; }
  drag.set(it.id, { state: "preparing", path: "" });
  renderFiles();
  try {
    const path = await shell.prepareDrag({ id: it.id, holder: it.holder, local: it.local, name: it.name });
    drag.set(it.id, { state: "ready", path });
  } catch (e) {
    drag.delete(it.id);
    toast(friendly(e), true);
  }
  renderFiles();
}

function onDragStart(e, it) {
  e.preventDefault(); // the app shell starts a real drag with the file
  const d = drag.get(it.id);
  if (d && d.state === "ready") {
    shell.startDrag(d.path);
  } else if (d && d.state === "failed") {
    toast("This file is too big to drag out. Use Save instead.", true);
  } else {
    toast("Getting the file ready. Try dragging again in a moment.");
    prepareDrag(it);
  }
}

// dropping files onto the window shares them
let dragDepth = 0;
const hasFiles = (e) => e.dataTransfer && Array.from(e.dataTransfer.types || []).includes("Files");
window.addEventListener("dragenter", (e) => { if (hasFiles(e)) { dragDepth++; $("veil").classList.add("on"); } });
window.addEventListener("dragleave", (e) => { if (hasFiles(e) && --dragDepth <= 0) { dragDepth = 0; $("veil").classList.remove("on"); } });
window.addEventListener("dragover", (e) => { if (hasFiles(e)) e.preventDefault(); });
window.addEventListener("drop", (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  dragDepth = 0;
  $("veil").classList.remove("on");
  shareFiles(Array.from(e.dataTransfer.files));
});

$("search").addEventListener("input", (e) => { query = e.target.value; renderFiles(); });

$("addFiles").addEventListener("click", async () => {
  if (shell && shell.pickFiles) {
    const paths = await shell.pickFiles();
    if (paths && paths.length) addByPaths(paths);
  } else {
    $("filePicker").click();
  }
});
$("filePicker").addEventListener("change", (e) => {
  shareFiles(Array.from(e.target.files));
  e.target.value = "";
});

// ------------------------------------------------------------ pairing

const pairDialog = $("pairDialog");
let inviteTimer = null;

function showTab(which) {
  const show = which === "show";
  $("tabShow").setAttribute("aria-selected", show);
  $("tabEnter").setAttribute("aria-selected", !show);
  $("paneShow").hidden = !show;
  $("paneEnter").hidden = show;
}
$("tabShow").addEventListener("click", () => { showTab("show"); startInvite(); });
$("tabEnter").addEventListener("click", () => { showTab("enter"); stopInvite(); $("joinText").focus(); });

function openPair() {
  showTab("show");
  pairDialog.showModal();
  startInvite();
}

pairDialog.addEventListener("close", stopInvite);

async function startInvite() {
  $("inviteTimer").textContent = "";
  try {
    const inv = await api("POST", "/api/invite", {});
    drawInvite(inv);
  } catch (e) {
    $("qr").replaceChildren();
    $("inviteText").value = "";
    $("inviteTimer").textContent = friendly(e);
  }
}

function drawInvite(inv) {
  const qr = qrcode(0, "L");
  qr.addData(inv.text);
  qr.make();
  $("qr").innerHTML = qr.createSvgTag({ scalable: true, margin: 0 });
  $("inviteText").value = inv.text;
  clearInterval(inviteTimer);
  const tick = () => {
    const s = Math.round((Date.parse(inv.expires) - Date.now()) / 1000);
    if (s <= 0) {
      $("inviteTimer").textContent = "This code has expired. Close and open Add device for a new one.";
      clearInterval(inviteTimer);
      return;
    }
    $("inviteTimer").textContent = `Works for another ${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}, for one device.`;
  };
  tick();
  inviteTimer = setInterval(tick, 1000);
}

function stopInvite() {
  clearInterval(inviteTimer);
  if (state.invite || !$("paneShow").hidden) api("DELETE", "/api/invite").catch(() => {});
}

$("copyInvite").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText($("inviteText").value);
    toast("Code copied");
  } catch (_) {
    $("inviteText").select();
    toast("Press Ctrl+C to copy the selected code");
  }
});

$("joinBtn").addEventListener("click", async () => {
  const text = $("joinText").value.trim();
  if (!text) { toast("Paste the code first", true); return; }
  $("joinBtn").disabled = true;
  try {
    const r = await api("POST", "/api/join", { invite: text });
    pairDialog.close();
    $("joinText").value = "";
    toast(`Paired with ${r.name}`);
  } catch (e) {
    toast(friendly(e), true);
  } finally {
    $("joinBtn").disabled = false;
  }
});

// a device asks to pair with the code we showed
const requestDialog = $("requestDialog");
let pendingRequest = null;
function askPair(ev) {
  pendingRequest = ev.id;
  $("requestTitle").textContent = `${ev.name || "A device"} wants to pair`;
  if (!requestDialog.open) requestDialog.showModal();
}
async function answerPair(accept) {
  const id = pendingRequest;
  pendingRequest = null;
  requestDialog.close();
  try { await api("POST", "/api/invite/reply", { id, accept }); } catch (e) { toast(friendly(e), true); }
}
$("acceptBtn").addEventListener("click", () => answerPair(true));
$("declineBtn").addEventListener("click", () => answerPair(false));
requestDialog.addEventListener("cancel", (e) => { e.preventDefault(); answerPair(false); });

// removing a device
const removeDialog = $("removeDialog");
let removing = null;
function askRemove(p) {
  removing = p;
  $("removeTitle").textContent = `Remove ${p.name}?`;
  removeDialog.showModal();
}
$("removeBtn").addEventListener("click", async () => {
  const p = removing;
  removeDialog.close();
  try {
    await api("DELETE", `/api/peers/${encodeURIComponent(p.id)}`);
    toast(`Removed ${p.name}`);
  } catch (e) { toast(friendly(e), true); }
});

// ------------------------------------------------------------ live updates

async function refresh() {
  try {
    state = await api("GET", "/api/state");
    render();
  } catch (e) {
    console.error(e);
  }
}

function onEvent(ev) {
  if (ev.type === "changed") {
    refresh();
  } else if (ev.type === "pair-request") {
    askPair(ev);
  } else if (ev.type === "pair-done") {
    if (pairDialog.open) pairDialog.close();
    if (requestDialog.open) requestDialog.close();
    toast(ev.ok ? `Paired with ${ev.name}` : `Pairing did not finish: ${friendly(ev.error || "declined")}`, !ev.ok);
    refresh();
  }
}

function connect() {
  const es = new EventSource("/api/events");
  es.onmessage = (m) => { try { onEvent(JSON.parse(m.data)); } catch (_) { /* ignore */ } };
  es.onerror = () => { es.close(); setTimeout(connect, 2000); };
}

// ------------------------------------------------------------ start

try {
  const v = localStorage.getItem("ephdrop.ttl");
  if (v) $("ttl").value = v;
} catch (_) { /* storage may be unavailable */ }
$("ttl").addEventListener("change", () => {
  try { localStorage.setItem("ephdrop.ttl", $("ttl").value); } catch (_) { /* ignore */ }
});

refresh();
connect();
// time left and the fuses move on their own
setInterval(renderFiles, 15000);
