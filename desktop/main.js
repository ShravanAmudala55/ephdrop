"use strict";
// ephdrop desktop shell: starts the Go program (ephdropd), shows its page in a
// window, lives in the tray, and lets files be dragged out of the window.

const { app, BrowserWindow, Tray, Menu, nativeImage, ipcMain, dialog, shell } = require("electron");
const { spawn } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

const MAX_DRAG_BYTES = 1_000_000_000; // larger files use Save instead

let daemon = null;
let daemonInfo = null; // {url, port, id}
let origin = null;
let win = null;
let tray = null;
let quitting = false;

const cacheDir = () => path.join(app.getPath("temp"), "ephdrop-drag");

// ------------------------------------------------------------ the Go program

function daemonPath() {
  if (process.env.EPHDROPD) return process.env.EPHDROPD;
  const exe = process.platform === "win32" ? "ephdropd.exe" : "ephdropd";
  return app.isPackaged ? path.join(process.resourcesPath, "bin", exe) : path.join(__dirname, "bin", exe);
}

function startDaemon() {
  return new Promise((resolve, reject) => {
    const bin = daemonPath();
    if (!fs.existsSync(bin)) {
      reject(new Error(`Cannot find ${bin}. Build it first: see desktop/README.md`));
      return;
    }
    const args = [
      "-dir", path.join(app.getPath("userData"), "node"),
      "-name", os.hostname().replace(/\.local$/, ""),
      "-downloads", app.getPath("downloads"),
      "-exit-with-stdin",
    ];
    daemon = spawn(bin, args, { stdio: ["pipe", "pipe", "pipe"], windowsHide: true });
    let buf = "";
    let settled = false;
    daemon.stdout.on("data", (d) => {
      buf += d;
      const nl = buf.indexOf("\n");
      if (nl >= 0 && !settled) {
        settled = true;
        try {
          resolve(JSON.parse(buf.slice(0, nl)));
        } catch (e) {
          reject(new Error("ephdropd printed something unexpected"));
        }
      }
    });
    let err = "";
    daemon.stderr.on("data", (d) => { err = (err + d).slice(-2000); });
    daemon.on("error", (e) => { if (!settled) { settled = true; reject(e); } });
    daemon.on("exit", (code) => {
      daemon = null;
      if (!settled) { settled = true; reject(new Error(`ephdropd stopped (${code}). ${err}`)); }
      else if (!quitting) {
        dialog.showErrorBox("ephdrop stopped", `The background program stopped unexpectedly.\n${err}`);
        app.quit();
      }
    });
  });
}

function stopDaemon() {
  return new Promise((resolve) => {
    if (!daemon) return resolve();
    const d = daemon;
    const t = setTimeout(() => { d.kill(); resolve(); }, 4000);
    d.once("exit", () => { clearTimeout(t); resolve(); });
    try { d.stdin.end(); } catch (_) { d.kill(); }
  });
}

// authenticated request to the Go program, for the shell's own needs
async function daemonFetch(p, opts = {}) {
  const token = new URL(daemonInfo.url).searchParams.get("token");
  const headers = { Authorization: `Bearer ${token}`, ...(opts.headers || {}) };
  return fetch(`${origin}${p}`, { ...opts, headers });
}

// ------------------------------------------------------------ window and tray

function createWindow() {
  win = new BrowserWindow({
    width: 480, height: 720, minWidth: 360, minHeight: 480,
    title: "ephdrop",
    icon: path.join(__dirname, "assets", "icon.png"),
    show: false,
    backgroundColor: "#e9eef1",
    webPreferences: {
      preload: path.join(__dirname, "preload.js"),
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      spellcheck: false,
    },
  });
  win.removeMenu();
  win.once("ready-to-show", () => win.show());
  win.loadURL(daemonInfo.url);

  // The window only ever shows our own page.
  win.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
  win.webContents.on("will-navigate", (e, url) => {
    if (new URL(url).origin !== origin) e.preventDefault();
  });
  // Closing hides the window. The program keeps sharing from the tray.
  win.on("close", (e) => {
    if (!quitting) { e.preventDefault(); win.hide(); }
  });
}

function showWindow() {
  if (!win) return;
  if (win.isMinimized()) win.restore();
  win.show();
  win.focus();
}

function createTray() {
  const file = process.platform === "darwin" ? "trayTemplate.png" : "tray.png";
  const img = nativeImage.createFromPath(path.join(__dirname, "assets", file));
  if (process.platform === "darwin") img.setTemplateImage(true);
  tray = new Tray(img);
  tray.setToolTip("ephdrop");
  tray.setContextMenu(Menu.buildFromTemplate([
    { label: "Open ephdrop", click: showWindow },
    { type: "separator" },
    { label: "Quit", click: () => app.quit() },
  ]));
  tray.on("click", showWindow);
}

// ------------------------------------------------------------ talking to the page

function fromOurPage(event) {
  try {
    return new URL(event.senderFrame.url).origin === origin;
  } catch (_) {
    return false;
  }
}

function safeName(name) {
  const n = path.basename(String(name)).replace(/[<>:"/\\|?*\x00-\x1f]/g, "_").trim();
  return n && n !== "." && n !== ".." ? n.slice(0, 200) : "file";
}

ipcMain.handle("prepare-drag", async (event, req) => {
  if (!fromOurPage(event)) throw new Error("not allowed");
  const id = String(req.id || "");
  if (!/^[a-z2-7]{26}$/.test(id)) throw new Error("bad file id");
  const dir = path.join(cacheDir(), id);

  const existing = fs.existsSync(dir) ? fs.readdirSync(dir).filter((f) => !f.startsWith(".part-")) : [];
  if (existing.length) return path.join(dir, existing[0]);
  fs.mkdirSync(dir, { recursive: true });

  if (req.local) {
    const res = await daemonFetch(`/api/files/${id}`);
    if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || "Could not read the file");
    const target = path.join(dir, safeName(req.name));
    const part = path.join(dir, ".part-" + process.pid);
    const out = fs.createWriteStream(part);
    const { Readable } = require("stream");
    const { pipeline } = require("stream/promises");
    await pipeline(Readable.fromWeb(res.body), out);
    fs.renameSync(part, target);
    return target;
  }
  const res = await daemonFetch("/api/fetch", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ holder: String(req.holder), id, dir }),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    fs.rmSync(dir, { recursive: true, force: true });
    throw new Error(data.error || "Could not get the file");
  }
  return data.path;
});

ipcMain.on("start-drag", (event, file) => {
  if (!fromOurPage(event)) return;
  const resolved = path.resolve(String(file));
  // only files we prepared ourselves
  if (!resolved.startsWith(cacheDir() + path.sep) || !fs.existsSync(resolved)) return;
  event.sender.startDrag({ file: resolved, icon: path.join(__dirname, "assets", "dragicon.png") });
});

ipcMain.handle("pick-files", async (event) => {
  if (!fromOurPage(event)) throw new Error("not allowed");
  const r = await dialog.showOpenDialog(win, { title: "Choose files to share", properties: ["openFile", "multiSelections"] });
  return r.canceled ? [] : r.filePaths;
});

ipcMain.on("show-in-folder", (event, file) => {
  if (!fromOurPage(event)) return;
  const p = String(file);
  if (path.isAbsolute(p) && fs.existsSync(p)) shell.showItemInFolder(p);
});

ipcMain.on("shell-info", (event) => {
  event.returnValue = { maxDragBytes: MAX_DRAG_BYTES };
});

// ------------------------------------------------------------ life cycle

if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  app.on("second-instance", showWindow);
  app.on("activate", showWindow);

  app.whenReady().then(async () => {
    fs.rmSync(cacheDir(), { recursive: true, force: true });
    try {
      daemonInfo = await startDaemon();
    } catch (e) {
      dialog.showErrorBox("ephdrop could not start", String(e.message || e));
      app.quit();
      return;
    }
    origin = new URL(daemonInfo.url).origin;
    createWindow();
    createTray();
  });

  app.on("window-all-closed", (e) => e.preventDefault()); // stay in the tray

  app.on("before-quit", () => { quitting = true; });
  app.on("will-quit", (e) => {
    if (daemon) {
      e.preventDefault();
      stopDaemon().then(() => {
        fs.rmSync(cacheDir(), { recursive: true, force: true });
        app.quit();
      });
    } else {
      fs.rmSync(cacheDir(), { recursive: true, force: true });
    }
  });
}
