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

function render() {
  $("selfName").textContent = state.self.name || "";
  renderDevices();
  renderFiles();
}

function renderDevices() {
  const ul = $("devices");
  ul.replaceChildren();
  for (const p of state.peers) {
    const li = el("li");
    const b = el("button", "chip " + (p.online ? "online" : "offline"));
    b.type = "button";
    b.append(el("span", "dot"), el("span", "", p.name));
    b.title = p.online ? `${p.name} is online. Click to remove it.` : `${p.name} is not reachable right now. Click to remove it.`;
    b.addEventListener("click", () => askRemove(p));
    li.append(b);
    ul.append(li);
  }
  const add = el("li");
  const ab = el("button", "chip add", "Add device");
  ab.type = "button";
  ab.addEventListener("click", openPair);
  add.append(ab);
  ul.append(add);
}

function renderFiles() {
  const ul = $("files");
  const empty = $("empty");
  const now = Date.now();
  const items = state.items.filter((it) => Date.parse(it.expires) > now);
  ul.replaceChildren();
  for (const it of items) ul.append(fileRow(it, now));
  $("hint").hidden = !(shell && items.length);

  empty.replaceChildren();
  empty.hidden = items.length > 0;
  if (items.length === 0) {
    if (state.peers.length === 0) {
      empty.append(el("h2", "", "No devices yet"), el("p", "", "Add your phone or another computer, then drop files here to share them. Files disappear on their own after a day."));
      const b = el("button", "primary", "Add device");
      b.addEventListener("click", openPair);
      empty.append(b);
    } else {
      empty.append(el("h2", "", "Nothing shared"), el("p", "", "Drop files anywhere in this window. Your other devices can pick them up until they expire."));
    }
  }
}

function fileRow(it, now) {
  const total = Math.max(1, Date.parse(it.expires) - Date.parse(it.created));
  const ms = Date.parse(it.expires) - now;
  const frac = Math.min(1, Math.max(0, ms / total));
  const level = ms < 3600e3 ? "low" : ms < 6 * 3600e3 ? "mid" : "";
  const d = drag.get(it.id);

  const li = el("li", "file" + (!it.local && !it.reachable ? " gone" : "") + (d && d.state === "preparing" ? " preparing" : ""));
  li.dataset.id = it.id;
  li.style.setProperty("--left", frac.toFixed(4));

  li.append(el("div", "tile", ext(it.name)));

  const main = el("div", "main");
  main.append(el("div", "name", it.name));
  main.firstChild.title = it.name;
  const sub = el("div", "sub");
  sub.append(el("span", "", it.local ? "On this device" : `From ${it.holderName || "another device"}`));
  sub.append(el("span", "left " + level, timeLeft(ms)));
  if (!it.local && !it.reachable) sub.append(el("span", "", "Not reachable right now"));
  if (shell && d && d.state === "ready") sub.append(el("span", "", "Ready to drag out"));
  main.append(sub);
  li.append(main);

  const act = el("div", "act");
  act.append(el("span", "size", size(it.size)));
  if (it.local) {
    if (!shell) {
      const a = el("a", "linkish", "Download");
      a.href = `/api/files/${encodeURIComponent(it.id)}`;
      act.append(a);
    }
    const rm = el("button", "", "Remove");
    rm.type = "button";
    rm.addEventListener("click", () => removeFile(it));
    act.append(rm);
  } else if (saved.has(it.id)) {
    act.append(el("span", "size", "Saved"));
    if (shell) {
      const show = el("button", "linkish", "Show");
      show.type = "button";
      show.addEventListener("click", () => shell.showInFolder(saved.get(it.id)));
      act.append(show);
    }
  } else {
    const sv = el("button", "primary", saving.has(it.id) ? "Saving" : "Save");
    sv.type = "button";
    sv.disabled = !it.reachable || saving.has(it.id);
    sv.addEventListener("click", () => saveFile(it));
    act.append(sv);
  }
  li.append(act);

  const fuse = el("div", "fuse " + level);
  fuse.setAttribute("aria-hidden", "true");
  li.append(fuse);

  if (shell && (it.local || it.reachable)) {
    li.classList.add("draggable");
    li.draggable = true;
    li.addEventListener("pointerenter", () => prepareDrag(it));
    li.addEventListener("focusin", () => prepareDrag(it));
    li.addEventListener("dragstart", (e) => onDragStart(e, it));
  }
  return li;
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
