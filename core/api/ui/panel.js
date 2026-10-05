"use strict";
// The tray panel: recent files at a glance. Everything else lives in the window.
const shell = window.ephdropShell || null;
const $ = (id) => document.getElementById(id);
const SVGNS = "http://www.w3.org/2000/svg";
let state = { self: {}, peers: [], items: [] };

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function short(ms) {
  if (ms <= 0) return "Expired";
  const m = Math.floor(ms / 60000), h = Math.floor(m / 60), d = Math.floor(h / 24);
  if (d >= 1) return h % 24 ? `${d}d ${h % 24}h` : `${d}d`;
  if (h >= 1) return h >= 6 || m % 60 === 0 ? `${h}h` : `${h}h ${m % 60}m`;
  if (m >= 1) return `${m}m`;
  return "<1m";
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

function devClass(it) {
  if (it.local) return "dev-self";
  const i = state.peers.findIndex((p) => p.id === it.holder);
  return "dev-" + (i < 0 ? 0 : i % 5);
}

function render() {
  const now = Date.now();
  const items = state.items
    .filter((it) => Date.parse(it.expires) > now)
    .sort((a, b) => Date.parse(b.created) - Date.parse(a.created))
    .slice(0, 8);
  const n = state.peers.length;
  $("status").textContent = n === 0 ? "No devices paired yet" : n === 1 ? "Sharing with 1 device" : `Sharing with ${n} devices`;
  const ul = $("list");
  ul.replaceChildren();
  $("empty").hidden = items.length > 0;
  for (const it of items) {
    const ms = Date.parse(it.expires) - now;
    const total = Math.max(1, Date.parse(it.expires) - Date.parse(it.created));
    const li = el("li", [devClass(it), ms < 3600e3 ? "low" : ""].filter(Boolean).join(" "));
    li.append(ring(Math.min(1, Math.max(0, ms / total))));
    const t = el("div", "t");
    const nm = el("span", "n", it.name);
    nm.title = it.name;
    t.append(nm, el("span", "f", it.local ? "This device" : (it.holderName || "Another device")));
    li.append(t, el("span", "tl", short(ms)));
    li.addEventListener("click", () => shell && shell.openMain());
    ul.append(li);
  }
}

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error(`Request failed (${res.status})`);
  return res.status === 204 ? null : res.json();
}

async function refresh() {
  try { state = await api("GET", "/api/state"); render(); } catch (e) { console.error(e); }
}

function connect() {
  const es = new EventSource("/api/events");
  es.onmessage = (m) => { try { if (JSON.parse(m.data).type === "changed") refresh(); } catch (_) { /* ignore */ } };
  es.onerror = () => { es.close(); setTimeout(connect, 2000); };
}

$("open").addEventListener("click", () => shell && shell.openMain());
$("quit").addEventListener("click", () => shell && shell.quit());
$("add").addEventListener("click", async () => {
  if (!shell) return;
  const paths = await shell.pickFiles();
  for (const path of paths || []) {
    try { await api("POST", "/api/share", { path, ttlHours: 24 }); } catch (e) { console.error(e); }
  }
  refresh();
});
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && shell) shell.hidePanel(); });

refresh();
connect();
setInterval(render, 30000);
