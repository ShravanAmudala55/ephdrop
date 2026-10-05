"use strict";
// Starts the real desktop app (needs a display, e.g. xvfb-run) and checks the
// parts that talk to the operating system. Run: EPHDROPD=/path/to/ephdropd npm test
const { _electron: electron } = require("playwright-core");
const { spawn } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");
const assert = require("assert");

const bin = process.env.EPHDROPD;
if (!bin) throw new Error("set EPHDROPD to the ephdropd program");

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ephdrop-smoke-"));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(fn, what, ms = 15000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error("timed out: " + what);
    await sleep(100);
  }
}

function startPeer() {
  return new Promise((resolve, reject) => {
    const p = spawn(bin, ["-dir", path.join(tmp, "peer"), "-name", "Phone", "-downloads", path.join(tmp, "peer-dl"), "-listen", "127.0.0.1:0", "-exit-with-stdin"], { stdio: ["pipe", "pipe", "inherit"] });
    let buf = "";
    p.stdout.on("data", (d) => { buf += d; if (buf.includes("\n")) resolve({ p, info: JSON.parse(buf.split("\n")[0]) }); });
    p.on("error", reject);
  });
}

(async () => {
  const peer = await startPeer();
  const token = new URL(peer.info.url).searchParams.get("token");
  const peerApi = async (method, p, body, raw) => {
    const r = await fetch(`http://127.0.0.1:${peer.info.port}${p}`, {
      method, headers: { Authorization: `Bearer ${token}`, ...(body ? { "Content-Type": "application/json" } : {}) },
      body: raw !== undefined ? raw : body ? JSON.stringify(body) : undefined,
    });
    const t = await r.text();
    return { status: r.status, body: t ? JSON.parse(t) : null };
  };

  const app = await electron.launch({
    args: [path.join(__dirname, ".."), "--no-sandbox", `--user-data-dir=${path.join(tmp, "user")}`],
    env: { ...process.env, EPHDROPD: bin },
  });
  const win = await app.firstWindow();
  await win.waitForSelector("#files", { state: "attached" });
  console.log("ok  window opened with the page");

  const keys = await win.evaluate(() => Object.keys(window.ephdropShell).sort());
  assert.deepStrictEqual(keys, ["maxDragBytes", "pathForFile", "pickFiles", "prepareDrag", "showInFolder", "startDrag"]);
  assert.strictEqual(await win.evaluate(() => typeof window.require + typeof window.process), "undefinedundefined");
  console.log("ok  the page gets only the narrow shell API and no Node");

  // pair with the peer
  await win.click("#devices .chip.add");
  await until(() => win.inputValue("#inviteText").then((v) => v.length > 20), "invite");
  const invite = await win.inputValue("#inviteText");
  const joining = peerApi("POST", "/api/join", { invite });
  await win.waitForSelector("#requestDialog[open]");
  await win.click("#acceptBtn");
  assert.strictEqual((await joining).status, 200);
  await until(() => win.locator("#devices .chip:not(.add)").count().then((c) => c === 1), "paired chip");
  console.log("ok  paired through the window");

  const state = await win.evaluate(() => fetch("/api/state").then((r) => r.json()));
  const pstate = (await peerApi("GET", "/api/state")).body;
  await peerApi("POST", `/api/peers/${state.self.id}/hint`, { addr: `127.0.0.1:${state.self.port}` });
  await win.evaluate(([id, port]) => fetch(`/api/peers/${id}/hint`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ addr: `127.0.0.1:${port}` }) }), [pstate.self.id, pstate.self.port]);

  // a file from the peer shows up, and can be prepared for dragging out
  await peerApi("PUT", "/api/upload?name=from-phone.txt", null, "text from the phone");
  const remoteRow = win.locator(".file", { hasText: "from-phone.txt" });
  await remoteRow.waitFor({ timeout: 15000 });
  await remoteRow.hover();
  await win.waitForSelector(".file:has-text('from-phone.txt') >> text=Ready to drag out", { timeout: 15000 });
  const cacheRoot = path.join(os.tmpdir(), "ephdrop-drag");
  const remoteFiles = fs.readdirSync(cacheRoot).flatMap((d) => fs.readdirSync(path.join(cacheRoot, d)).map((f) => path.join(cacheRoot, d, f)));
  const remoteCopy = remoteFiles.find((f) => f.endsWith("from-phone.txt"));
  assert.ok(remoteCopy, "dragged copy of the remote file exists with its real name");
  assert.strictEqual(fs.readFileSync(remoteCopy, "utf8"), "text from the phone");
  console.log("ok  a remote file is fetched and ready to drag out under its real name");

  // a local file: shared by path, then prepared for dragging
  const mine = path.join(tmp, "my notes.txt");
  fs.writeFileSync(mine, "my own words");
  await win.evaluate((p) => fetch("/api/share", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: p }) }), mine);
  const localRow = win.locator(".file", { hasText: "my notes.txt" });
  await localRow.waitFor();
  await localRow.hover();
  try {
    await win.waitForSelector(".file:has-text('my notes.txt') >> text=Ready to drag out", { timeout: 8000 });
  } catch (e) {
    const items = await win.evaluate(() => fetch("/api/state").then((r) => r.json()).then((s) => s.items));
    const it = items.find((i) => i.name === "my notes.txt");
    console.log("item", it);
    console.log("prepare says:", await win.evaluate((it) => window.ephdropShell.prepareDrag(it).then((p) => "ok " + p, (e) => "error " + e.message), it));
    throw e;
  }
  const localCopy = fs.readdirSync(cacheRoot).flatMap((d) => fs.readdirSync(path.join(cacheRoot, d)).map((f) => path.join(cacheRoot, d, f))).find((f) => f.endsWith("my notes.txt"));
  assert.ok(localCopy);
  assert.strictEqual(fs.readFileSync(localCopy, "utf8"), "my own words");
  console.log("ok  a local file is ready to drag out under its real name");

  // the peer sees our file too
  await until(async () => (await peerApi("GET", "/api/state")).body.items.some((i) => i.name === "my notes.txt"), "peer sees my file");
  console.log("ok  the other device sees the file shared from the window");

  await win.screenshot({ path: path.join(tmp, "window.png") });
  fs.copyFileSync(path.join(tmp, "window.png"), process.env.SHOT || "/tmp/desktop-window.png");

  // navigation away from our page is blocked
  const url0 = win.url();
  await win.evaluate(() => { location.href = "https://example.com/"; });
  await sleep(500);
  assert.strictEqual(win.url(), url0);
  console.log("ok  the window cannot be sent to another site");

  // closing the window keeps the app (and sharing) alive
  const visible = () => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].isVisible());
  await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].close());
  await sleep(300);
  assert.strictEqual(await visible(), false);
  const stillThere = (await peerApi("GET", "/api/state")).body.peers[0];
  assert.ok(stillThere);
  await until(async () => (await peerApi("GET", "/api/state")).body.peers[0].online, "still online after closing the window");
  console.log("ok  closing the window hides it; the device stays online");

  // quitting stops the Go program
  const pid = app.process().pid;
  await app.evaluate(({ app }) => app.quit());
  await until(() => { try { process.kill(pid, 0); return false; } catch (_) { return true; } }, "app exit");
  await until(async () => !(await peerApi("GET", "/api/state")).body.peers[0].online, "peer notices", 150000).catch(() => {});
  assert.ok(!fs.existsSync(cacheRoot), "drag cache removed on quit");
  console.log("ok  quitting removes the temporary drag copies");

  peer.p.stdin.end();

  // Dragging itself: startDrag accepts a prepared file and ignores anything
  // else. On Linux it starts a real drag that holds the window until a mouse
  // button is released, so it gets an app of its own, which is then killed.
  const app2 = await electron.launch({
    args: [path.join(__dirname, ".."), "--no-sandbox", `--user-data-dir=${path.join(tmp, "user2")}`],
    env: { ...process.env, EPHDROPD: bin },
  });
  const win2 = await app2.firstWindow();
  await win2.waitForSelector("#files", { state: "attached" });
  const prepared = await win2.evaluate(async () => {
    const e = await fetch("/api/upload?name=drag-me.txt", { method: "PUT", body: "drag" }).then((r) => r.json());
    return window.ephdropShell.prepareDrag({ id: e.id, holder: "", local: true, name: "drag-me.txt" });
  });
  assert.ok(fs.existsSync(prepared));
  await win2.evaluate(() => window.ephdropShell.startDrag("/etc/passwd"));
  await win2.evaluate((p) => window.ephdropShell.startDrag(p), prepared);
  console.log("ok  startDrag did not throw (the drop itself needs a person to check)");
  process.kill(app2.process().pid, "SIGKILL");
  await sleep(500);
  fs.rmSync(tmp, { recursive: true, force: true });
  console.log("all desktop checks passed");
})().catch((e) => { console.error("FAILED:", e); process.exit(1); });
