"use strict";
// What the page may ask the shell to do. Nothing here gives the page general
// access to the computer: each call does one narrow thing and main.js checks it.
const { contextBridge, ipcRenderer, webUtils } = require("electron");

const info = ipcRenderer.sendSync("shell-info");

contextBridge.exposeInMainWorld("ephdropShell", {
  maxDragBytes: info.maxDragBytes,
  // the path of a file dropped onto the window
  pathForFile: (file) => webUtils.getPathForFile(file),
  // make a real file with its real name ready, so it can be dragged out
  prepareDrag: (item) => ipcRenderer.invoke("prepare-drag", {
    id: item.id, holder: item.holder, local: !!item.local, name: item.name,
  }),
  startDrag: (path) => ipcRenderer.send("start-drag", path),
  pickFiles: () => ipcRenderer.invoke("pick-files"),
  showInFolder: (path) => ipcRenderer.send("show-in-folder", path),
});
