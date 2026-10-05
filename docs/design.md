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

Implemented in `core/discovery`, in two layers.

**Announcement format** (the same on every platform). Devices publish a DNS-SD service of type `_ephdrop._tcp` in the `local.` domain:

- instance name: `ephdrop-` plus the first 12 characters of the device id
- port: the port the device listens on
- TXT record: `v=1` (protocol version) and `id=<device id>`

The device's own addresses come from the mDNS layer's normal A and AAAA records. Announcements that are malformed, from a newer protocol version, or that carry no local address are dropped. Only private, link-local and loopback addresses are accepted, so a hostile device cannot make us connect (and show our certificate) to an address on the internet.

**Finder**. Keeps the set of paired devices that are visible now, with their addresses:

- Only paired ids are tracked, and our own id is ignored, so strangers' announcements use no memory.
- A device that goes quiet is dropped after 2 minutes (the usual mDNS record lifetime). A goodbye removes it at once.
- Changes are delivered as `Appeared`, `Updated` and `Disappeared` events. A slow subscriber never blocks the Finder and can resync with `Peers()`.
- `Hint(id, addrs)` lets the user type an address by hand for networks where announcements do not get through (routers with client isolation, VPNs). Hints never expire. After pairing, the joiner can hint the inviter's invite addresses.
- When a device is unpaired it disappears from the Finder immediately, and `Forget` clears what is stored.

**Backends** do the actual sending and receiving. The core has a `Backend` interface (`Advertise`, `Browse`), and native code can skip it and call `Finder.Seen` and `Finder.Gone` directly.

| Platform | Backend |
|----------|---------|
| Windows, Mac | Go mDNS library (planned) |
| Android | Android NSD, or the Go library with a multicast lock held (to decide) |
| iOS | System Bonjour (`NWBrowser`, `NetService`), reporting into `Finder.Seen` |

Why iOS cannot use a Go mDNS library: on iOS, sending or receiving raw multicast needs the `com.apple.developer.networking.multicast` entitlement, which Apple grants on request and which SideStore builds do not have. Bonjour through the system APIs does not need it. It does need the user's Local Network permission, `NSLocalNetworkUsageDescription` and `_ephdrop._tcp` listed under `NSBonjourServices` in Info.plist. Opening a connection to a local address also needs that permission, and if the app is in the background while the permission is still undecided, iOS denies the operation without asking. This fits the plan to treat iOS as a foreground client.

**Announcements are not authenticated.** Anyone on the network can claim any id. That is safe because discovery only says where to try. The connection is pinned to the paired device's key, so a device that lies about its id fails the TLS handshake.

**Known trade-off:** the id in the TXT record is stable, so someone on the same Wi-Fi can tell that a device with that id is present. Rotating beacons would hide this at the cost of complexity. See the open questions in TRACKING.md.

## Pairing

One time, by QR code. Implemented in `core/pairing`.

The invite (shown as a QR code, text form `ephdrop://pair/...`, about 190 characters) holds:

- the inviter's public key
- up to 8 `host:port` addresses on the local network
- a random 128 bit secret
- an expiry time (5 minutes by default)

Steps:

1. Device A (inviter) listens and shows the invite.
2. Device B (joiner) scans it and connects to A over TLS 1.3. B accepts only the key from the QR code, so a device that merely sits at A's address is refused before any secret is sent. The QR code is the trusted channel.
3. B sends `hello` with its name and a proof: HMAC-SHA256 over the secret, a role label, the TLS session's exported keying material, and both public keys. Binding the proof to the TLS session means a recorded proof cannot be replayed on another connection.
4. A checks the proof. Only if it is valid does A ask its user "pair with B?". A device without the QR code never gets a prompt shown.
5. If the user agrees, A saves B, then replies `accept` with its own proof so B knows A also holds the secret. B saves A.
6. The invite is used up. Pairing another device needs a new invite.

Limits and safeguards:

- Five wrong secrets close the invite. Connections that are not valid TLS or not valid messages are ignored and do not count.
- Messages are single lines of JSON, capped at 4 KB. Names from the other device are stripped of control characters and capped at 64 characters.
- The user's decline is final for that invite.
- A peer's stored id must equal the hash of its stored key, both when adding and when loading the file.

After pairing, devices connect with `identity.PinnedConfig` and `Store.Allow`, so only paired keys can connect. Peers are kept in `peers.json` (owner only permissions). `ephdrop unpair` or `Store.Remove` forgets a device, and re-pairing is needed to rejoin.

Not covered by the protocol: showing and scanning the QR code. The core produces and parses the invite string, and each client renders and scans it.

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
