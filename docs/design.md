# ephdrop design

Draft 1, 2026-10-05. Everything here can change.

## Goals

1. Share files between iPhone, Android, Windows and Mac on one home network.
2. No server, no account, no cloud.
3. Files delete themselves after a set time.
4. A device downloads a file only when the user asks for it.

## Components

```
+---------------------------+
| Client app (per platform) |  UI, share sheet, background service
+-------------+-------------+
              |
+-------------v-------------+
| core (Go)                 |
|  identity   pairing       |
|  discovery  file list     |
|  transport  expiry        |
+---------------------------+
```

## Identity

Each device generates an ed25519 keypair on first run. The public key hash is the device id. The private key never leaves the device.

## Discovery

Devices advertise `_ephdrop._tcp` over mDNS (Bonjour on Apple platforms) with:

- device id (short)
- port
- protocol version

Only the id and port are public. Nothing about files is advertised.

Known issue: some routers isolate Wi-Fi clients and block mDNS. A manual "add by IP" option is a fallback.

## Pairing

One time, by QR code.

1. Device A shows a QR code containing: its device id, its public key, its address and port, and a random 128 bit pairing secret.
2. Device B scans it and connects to A over TLS.
3. B proves it has the secret (HMAC over both public keys) and sends its own public key.
4. A verifies, shows "pair with B?" for confirmation, and both store each other's public keys.
5. The pairing secret is discarded.

After this, devices only talk to peers whose public keys they have stored. Connections use TLS with pinned peer keys, so other people on the Wi-Fi cannot read or join.

Removing a device deletes its stored key. Re-pairing is required to rejoin.

## File list

Every device keeps a list. Each entry:

| Field | Meaning |
|-------|---------|
| id | random 128 bit id |
| name | file name |
| size | bytes |
| hash | sha256 of content |
| owner | device id that added the file |
| created | unix time |
| expires | unix time |
| deleted | tombstone flag |

Lists are merged between peers on connect and when a file is added. Entries are immutable except for the tombstone flag. Merge is a union by id. A tombstone wins over a live entry.

Expiry is absolute time, set when the file is added. Clocks can differ a little between devices, so deletion happens when the local clock passes `expires`. A small grace period is fine.

## Transfer

1. User taps a file in the list.
2. The device asks a peer that holds the content (starting with the owner) over TLS: `GET /files/{id}`.
3. The file streams with range support so interrupted downloads resume.
4. The receiver checks the sha256 against the list entry.
5. The file is stored locally until it expires or the user deletes it.

Open question: whether peers that already pulled a file also serve it.

## Expiry

A sweeper runs on start and about every minute while the app is running:

- Delete the stored content of expired entries.
- Mark entries as tombstones, and drop tombstones after a longer retention (for example 7 days) so late peers learn about the deletion.

On iOS the sweeper also runs when the app opens.

## Platform notes

- Windows and Mac: background process with a tray or menu bar icon. Starts at login.
- Android: foreground service with a notification. Needs battery optimisation off. Share sheet target for adding files.
- iOS: foreground only. Uses local network permission (needs `NSLocalNetworkUsageDescription` and the Bonjour service type in Info.plist). Share extension adds files to a pending list that is announced next time the app opens. Built as an .ipa for SideStore, with the 7 day free signing limit handled by SideStore refresh.

## Security notes

- All peer traffic is encrypted and authenticated with pinned keys.
- The pairing secret is single use and short lived.
- No telemetry and no network calls outside the LAN.
- Files at rest are stored in the app sandbox. Optional encryption at rest is a later idea.
- To review before going public: pairing flow, replay protection, path handling for file names, size limits.

## Out of scope for now

- Internet access and relays
- Folders and nested paths (single files first)
- Versions, edits, conflict resolution
- Multiple households or user accounts
