# ephdrop tracking

Last updated: 2026-10-05

## Status

Phase: M1 in progress. Identity, pinned TLS and pairing are done. Next: mDNS discovery.

## Milestones

### M0: Project setup
- [x] Name chosen: ephdrop
- [x] Private repo created: github.com/ShravanAmudala55/ephdrop
- [x] README, tracking and design doc written
- [ ] Choose licence (see decisions)
- [ ] Decide core language binding approach (gomobile vs native per platform)

### M1: Go core, two peers on one machine
- [x] Go module set up in core/
- [x] Device identity (ed25519 keypair generated on first run, saved with 0600 permissions)
- [ ] mDNS discovery of peers on the LAN
- [x] Pairing by invite (shared secret, mutual proof bound to the TLS session, single use, expiry, attempt limit). QR rendering and scanning belong to the clients
- [x] Pinned TLS config: TLS 1.3, both sides present certs, peer key hash checked against paired ids (identity/tls.go)
- [ ] Encrypted transport server and client built on the pinned TLS config
- [ ] File list (id, name, size, hash, owner, created, expires)
- [ ] List gossip between paired peers
- [ ] Pull a file from a peer by id
- [ ] Expiry sweeper deletes expired files and entries
- [ ] Command line tool to drive the core for testing (done: `id`, `invite`, `join`, `peers`, `unpair`. To add: list, add, get, once the file list exists)
- [x] Tests for pairing (invites, store, full handshakes, attacks)
- [ ] Tests for list merge and expiry

### M2: Windows client
- [ ] Choose UI approach (see open questions)
- [ ] Show the invite as a QR code and scan one to pair
- [ ] Tray app with file list
- [ ] Drag and drop to add a file with expiry choice
- [ ] Download on click
- [ ] Runs at login in the background

### M3: Android client
- [ ] Core bound into the app
- [ ] Foreground service for background sync
- [ ] Share sheet target: share any file into ephdrop
- [ ] Battery optimisation guidance in the app
- [ ] Scan the QR code to pair (camera) and show our own invite
- [ ] Pair with Windows by QR code

### M4: iOS client
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

## Open questions

- Licence: MIT, Apache 2.0 or GPL?
- UI for desktop: native per platform, Flutter, or a small web UI served locally?
- Does the core run as a library inside each app (gomobile) or as a separate local daemon on desktop?
- Do files stay on the device that added them only, or can other devices that pulled a file also serve it?
- Maximum file size and per device storage cap?
- Should expiry be changeable after a file is added?

## Risks

- iOS app can only transfer while open, so the sending device must be on when pulling.
- Android vendors may kill background services, so sync can stop unless battery optimisation is turned off.
- Four platforms is a lot for one maintainer. Order of work reduces this: core, Windows, Android, then iOS and Mac.
- Local network permission on iOS and mDNS quirks on some routers (client isolation).

## Changelog

- 2026-10-05: Project created. README, tracking and design doc added.
- 2026-10-05: Pairing done (core/pairing, 31 tests, 50 across core, security checks verified by breaking each one on purpose). CLI gained `invite`, `join`, `peers`, `unpair`.
- 2026-10-05: M1 started. Added core/identity (keypair, device id, key storage, pinned TLS config) with 15 tests, plus `ephdrop id` command.
