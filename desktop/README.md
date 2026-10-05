# ephdrop desktop (Windows, Mac)

A small tray app. The window shows your files and your paired devices. You can:

- drop files anywhere in the window to share them with your paired devices,
- drag a file out of the window into any folder or app to copy it there,
- save a file from another device with one click.

Closing the window hides it. ephdrop keeps sharing from the tray until you choose Quit.

## How it is built

The app has two parts.

1. `ephdropd`, the Go program from `core/cmd/ephdropd`. It does everything: pairing, finding devices, sharing, transfers, expiry. It serves a page on this computer only.
2. This Electron shell. It starts `ephdropd`, shows its page in a window, adds the tray icon, and gives the page a few narrow abilities that a web page cannot have (see `preload.js`): the path of a dropped file, a file picker, and starting a drag out of the window.

Dragging a file out works like this. When you point at a file, the shell fetches it into a temporary folder under its real name. When you start dragging, the shell starts a normal operating system drag with that file. Files over 1 GB are not prepared for dragging (use Save). The temporary folder is emptied when the app quits.

## Run it

You need Node 20 or newer and Go 1.24 or newer.

```
cd desktop
npm install
npm start          # builds ephdropd for this computer, then opens the app
```

`npm test` starts the real app on a virtual screen and checks the parts that talk to the operating system (needs Linux with `xvfb`). To check dragging by hand, run the app, share a file on one device, point at it on another, and drag it onto the desktop.

## Package it

```
npm run dist
```

This builds an installer for the computer you run it on (a .dmg on a Mac, an installer on Windows). It is not signed, so the first time you open it, macOS asks you to confirm in System Settings, and Windows SmartScreen shows a warning ("More info", then "Run anyway").

## Status

Tested: window, pairing, sharing, saving, preparing files for dragging, closing to the tray, quitting, on Linux with a virtual screen. Not yet tested on a real Windows or Mac desktop: the drop itself, the tray icon, and packaging.
