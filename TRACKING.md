# ephdrop tracking

Last updated: 2026-10-05

## Status

Phase: M2 built, needs a trial on a real Windows and Mac desktop. Core (identity, pairing, discovery, shelf, transfer, file list, change pokes), local API, window UI and Electron tray app work and pass tests. Left in M1: resume of interrupted downloads, trying it on two real machines. Left in M2: run at login, scan a QR code on desktop, installer packaging check. Started M3 (Android): the Go side for phones is done and tested; the Kotlin app is not written yet.

## Milestones

### M0: Project setup
- [x] Name chosen: ephdrop
- [x] Private repo created: github.com/ShravanAmudala55/ephdrop
- [x] README, tracking and design doc written
- [ ] Choose licence (see decisions)
- [ ] Decide core language binding approach for mobile (gomobile vs native). Desktop uses a daemon

### M1: Go core, two peers on one machine
- [x] Go module set up in core/
- [x] Device identity (ed25519 keypair generated on first run, saved with 0600 permissions)
- [x] Discovery format and logic (core/discovery): announcement format, paired-only Finder, expiry, goodbye, manual address hints, events
- [x] mDNS backend for Windows and Mac (core/discovery/mdns, grandcat/zeroconf v1.0.0, vendored only for offline builds)
- [x] Pairing by invite (shared secret, mutual proof bound to the TLS session, single use, expiry, attempt limit). QR rendering and scanning belong to the clients
- [x] Pinned TLS config: TLS 1.3, both sides present certs, peer key hash checked against paired ids (identity/tls.go)
- [x] Transport server and client on the pinned TLS config (core/transfer): list and get, size and hash checks, limits, timeouts, cancel
- [x] File list and local storage (core/shelf): id, name, size, SHA-256, owner, created, expires. Files are copied into the shelf, ttl 1 day by default and 7 days at most, expired files are hidden at once
- [x] Combined file list across devices (core/board). Decision: lists are not passed on between devices, each file is offered only by its holder
- [x] Pull a file from a peer by id (resume of interrupted downloads still to do)
- [x] Expiry sweep (shelf.Sweep deletes expired files and entries). Still to do: call it on a timer from the running app
- [ ] Command line tool to drive the core for testing (done: `id`, `invite`, `join`, `peers`, `unpair`, `serve`, `list`, `get`. Limit: one process per data directory)
- [x] Tests for pairing (invites, store, full handshakes, attacks)
- [x] Tests for the combined list and expiry (core/board)

### M2: Desktop client (Windows and Mac)
- [x] Choose UI approach: Go daemon plus Electron shell (see decisions)
- [x] Show the invite as a QR code; paste an invite to join. Scanning a QR code on desktop is not built
- [x] Tray app with file list (window hides to the tray on close)
- [x] Drag and drop to add a file with expiry choice
- [x] Download on click, and drag a file out of the window to any folder or app
- [ ] Runs at login in the background
- [ ] Try on a real Windows and Mac desktop (tray icon, drag out, `npm run dist` installers)

### M3: Android client
- [x] Go side for phones (`core/mobile`): start and stop, local page address, discovery fed by the app, invites that use addresses the app supplies. Tested here, including two agents pairing and sharing, with deliberate-break checks. Not yet built into an .aar (needs gomobile and the Android SDK on a computer that can reach them)
- [x] Kotlin app written (clients/android): foreground service, web view of the window, file picker, share sheet target, QR scan button, save to Downloads/ephdrop, discovery through Android NSD. Written without being able to compile it
- [ ] Build it on a real computer: run build-lib.sh, open clients/android in Android Studio, fix whatever it complains about
- [x] Foreground service for background sync (written, untested)
- [x] Share sheet target: share any file into ephdrop (written, untested)
- [ ] Battery optimisation guidance in the app
- [x] Scan the QR code to pair (Google code scanner) and show our own invite (written, untested)
- [x] Discovery backend: Android NSD feeding the Go program (written, untested)
- [ ] Pair with Windows by QR code

### M4: iOS client
- [ ] Discovery through system Bonjour (`NWBrowser` and `NetService`) feeding `Finder.Seen`. Add `_ephdrop._tcp` to NSBonjourServices and write NSLocalNetworkUsageDescription
- [ ] SwiftUI app using the core
- [ ] Foreground only: connect on open, show list, tap to pull
- [ ] Share extension to drop files in
- [ ] Unsigned .ipa build for SideStore
- [ ] Scan the QR code to pair (camera) and show our own invite
- [ ] Local network permission flow tested

### M5: Mac client
- [ ] Menu bar app
- [ ] Same features as Windows

### M6: Release
- [ ] Choose licence and add LICENSE file
- [ ] Security review of pairing and transport
- [ ] Make the repo public
- [ ] Publish releases (Windows, Mac, Android apk, iOS ipa)
- [ ] README with install steps per platform

## Decisions

| Date | Decision | Reason |
|------|----------|--------|
| 2026-10-05 | Name is ephdrop | Short, describes ephemeral drop |
| 2026-10-05 | No server, peer to peer on the LAN | Matches the household use case, no cloud or accounts |
| 2026-10-05 | iOS is a foreground client only | iOS suspends background apps. Opening the app to pull is acceptable |
| 2026-10-05 | iOS distributed through SideStore | Not using the App Store |
| 2026-10-05 | Files expire after 1 to 2 days by default | Keeps storage small and avoids sync conflicts |
| 2026-10-05 | Core in Go | Existing Go experience, can be shared to mobile with gomobile |
| 2026-10-05 | Repo stays private until M6 | Make it public once it works and is reviewed |
| 2026-10-05 | Pairing proof is bound to the TLS session; invites last 5 minutes, work once, and close after 5 wrong secrets | Stops replay and guessing, and an attacker without the QR code never triggers a prompt |
| 2026-10-05 | Core returns the invite as text; clients draw and scan the QR code | Keeps the core free of UI and camera dependencies |
| 2026-10-05 | Discovery is pluggable: shared announcement format and Finder logic in the core, platform backends outside | iOS cannot use raw multicast without an Apple entitlement (checked against Apple TN3179), so it must use system Bonjour |
| 2026-10-05 | Only local-network addresses are accepted from announcements | A hostile device cannot make us connect to the internet |
| 2026-10-05 | Every platform gets its own app over the shared core, and any mix of devices works | Households have different devices, so no platform may depend on another |
| 2026-10-05 | Desktop app is a Go daemon (`ephdropd`) with a local HTTP API, shown by an Electron window and tray | A real file drag out of the window needs an OS file path, which a plain browser cannot give. The window UI is plain HTML served by the daemon |
| 2026-10-05 | Drag out works by preparing a temp copy under the real file name when the pointer is over a file | The OS drag needs a file on disk first. Copies are removed on quit, files over 1 GB are not prepared |
| 2026-10-05 | Local API accepts only a session cookie or bearer token, a known Host header and same-origin JSON posts | Stops other web pages and other local users from driving the daemon |
| 2026-10-05 | Devices tell paired peers when their list changes (`poke`) | New files appear on other devices at once instead of at the next 30s refresh |
| 2026-10-05 | Window design: white blueprint sheet (navy in dark mode), JetBrains Mono, sidebar of devices plus file table, a ring timer per file | Chosen by the maintainer from mockups. Rings show how much of a file's life is left; the last hour turns red |
| 2026-10-05 | Logo: lowercase e and capital D with a curved ribbon (behind the e, over the D) on a dusk tile; hand-drawn wordmark | Chosen by the maintainer. Files in design/logo |

## Open questions

- Licence: MIT, Apache 2.0 or GPL?
- Mobile: bind the core with gomobile on Android and iOS? (Desktop is decided: separate daemon.)
- Do files stay on the device that added them only, or can other devices that pulled a file also serve it?
- Maximum file size and per device storage cap?
- Should expiry be changeable after a file is added?

- Dependencies pulled in by zeroconf v1.0.0 are old (miekg/dns 1.1.27, x/net, x/sys, x/crypto). Consider `go get -u` for them and rerun the tests.
- Privacy of announcements: the device id in the TXT record is stable. Rotating beacons (a value derived from the id and the time, which paired devices can recognise) would stop outsiders on the Wi-Fi from tracking a device, at the cost of complexity.

## Risks

- iOS app can only transfer while open, so the sending device must be on when pulling.
- Android vendors may kill background services, so sync can stop unless battery optimisation is turned off.
- Four platforms is a lot for one maintainer. Order of work reduces this: core, Windows, Android, then iOS and Mac.
- Local network permission on iOS and mDNS quirks on some routers (client isolation). Manual address hints are the fallback.
- iOS cannot do raw multicast without an Apple entitlement, so discovery there depends on system Bonjour and the user granting Local Network access.

## Changelog

- 2026-10-05: Project created. README, tracking and design doc added.
- 2026-10-05: Discovery logic done (core/discovery, 31 tests, 81 across core, 21 deliberately broken versions all caught). Announcement format, Finder with paired-only tracking, expiry, hints and events. The real mDNS backend is not written yet.
- 2026-10-05: Pairing done (core/pairing, 31 tests, 50 across core, security checks verified by breaking each one on purpose). CLI gained `invite`, `join`, `peers`, `unpair`.
- 2026-10-05: M1 started. Added core/identity (keypair, device id, key storage, pinned TLS config) with 15 tests, plus `ephdrop id` command.
- 2026-10-05: mDNS backend done (core/discovery/mdns). Browse restarts every 30s because the library reports each device once per session and drops goodbyes. Falls back to IPv4-only when IPv6 multicast is missing. Advertises with a neutral host name and private addresses only, so the computer name and public IPs are not published. Tested with two devices over real multicast. Mutation checks caught all but a few cases that need a faulty library to reach.
- 2026-10-05: Dependencies pinned to versions that build on Go 1.24 (the 2019/2020 ones failed to link on newer macOS Go). Real mDNS test passes on the Mac. Local file shelf done (core/shelf): streaming copy with hash and size limit, names cleaned, index saved atomically, stray and missing files cleaned on open, expiry sweep. Deliberate-break checks caught every case except two that cannot be observed (file permission default and fsync).
- 2026-10-05: Transfer done (core/transfer) and wired into the CLI (`serve`, `list`, `get`). Two paired devices shared and downloaded a 3 MB file byte for byte in an end to end run. A server that is busy makes new clients wait rather than turning them away. Deliberate-break checks caught every case that can be observed. Not yet tried on two real machines.

- 2026-10-05: Combined file list done (core/board). It fetches each visible paired device's list when it appears and every 30s, hides expired files, marks files of unreachable devices, and pulls from the holder. Decided against passing lists on between devices. Checks caught every case that can be observed.
- 2026-10-05: Desktop app built (M2). Added core/node (one facade for apps), core/api (local HTTP API with cookie or token, Host and Origin checks, live events, upload and download), the window UI, `ephdropd`, and the Electron tray app in `desktop/`. Added a `poke` request so peers see new files at once. Smoke tests pass: window opens without Node access, pairing through the window, drag out files ready, peers see shared files, the window cannot navigate away, closing hides to the tray, quitting stops the daemon and removes temp copies. Not tested: a real OS drop onto the desktop, the tray icon on real Windows or Mac, installer packaging. Deliberate-break checks were not yet run on node, api and poke.
- 2026-10-05: New window design built into the app (core/api/ui): sidebar with device filters, searchable file table with ring timers, action bar for the selected file, dark mode, narrow layout. New logo, hand-drawn wordmark and app and tray icons (design/logo, desktop/assets). Go tests pass with the race detector and the Electron smoke test passes under a virtual display. The tray icons and the real file drop onto the desktop are still untested on a real Windows or Mac.
- 2026-10-05: Trial on a Mac and a Windows laptop: pairing, sharing and dragging between them all worked. Fixed what he found: the Mac menu bar icon was twice too big and a solid tile, so it is now a small template icon (the e and D with the ribbon, no tile). Clicking the tray icon now opens a small panel (recent files with ring timers, Add files, Open, Quit) instead of only a menu; right click still shows the menu. The Windows laptop showed the old design because the new design commit had not been pushed yet. Panel checked in the Electron smoke test; not yet seen on a real menu bar or Windows taskbar.
- 2026-10-05: The app is now always the white design. Before, it followed the system theme and turned navy on computers set to dark mode (which is why the Mac and Windows trial looked off). Navy is kept as an option that is off by default.
- 2026-10-05: Added 'Open at login' (right-click the tray icon; off by default; starts hidden in the tray). Works on Mac and Windows only, and the setting itself is not testable on the Linux test machine, so it needs a try on a real computer.
- 2026-10-05: Started M3. Added core/mobile, the one small Go interface both phone apps will use (start, stop, page address, discovery reports from the system, no direct interface listing). Added a way for the app to give its own addresses for invites, because Go on Android can be blocked from listing interfaces. Plan: the Android app is a thin Kotlin shell (foreground service, web view of the existing window, file picker, share sheet, QR scanner) so the window you already tried is reused. Cannot build an .apk on the test machine (no route to the Android SDK).
- 2026-10-05: Wrote the Android app (clients/android) and the build script. Android Studio, SDK, NDK and Go are installed on his Mac. The Kotlin cannot be compiled on the test machine, so it is untested; the Go side it uses (core/mobile) is tested and compiles for Android arm64. The window page gained a Scan QR button and a save-to-Downloads hook that only appear inside the Android app.
