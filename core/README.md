# core

Go library shared by all clients: identity, discovery, pairing, file list, transfer and expiry. See [../docs/design.md](../docs/design.md).

## Packages

| Package | Status | What it does |
|---------|--------|--------------|
| `identity` | done | ed25519 device key, device id, key storage, pinned TLS config |
| `pairing` | done | one time invites, paired device list, pairing handshake |
| `discovery` | logic done, mDNS backend in `discovery/mdns` | announcement format, Finder for visible paired devices, address hints |
| `shelf` | done | this device's shared files: copy in, hash, list, open, remove, expiry and sweep |
| `board` | done | the file list a user sees: own files plus other devices' files, reachability, pulling |
| `transfer` | done | list and pull files between paired devices over pinned TLS, with size and hash checks |
| `node` | done | one facade over all of the above for apps: start, share, fetch, pair, events |
| `api` | done | local HTTP API and embedded window UI used by the desktop app |
| `internal/atomicfile` | done | crash safe file writes |
| `cmd/ephdrop` | in progress | development command line driver |
| `cmd/ephdropd` | done | background daemon for the desktop app, see [../desktop](../desktop/README.md) |

## Try it

```
go test -race ./...
go build -o ephdrop ./cmd/ephdrop
```

Pair two devices (or two terminals, each with its own `-dir`):

```
# device A
./ephdrop -dir /tmp/a -name Laptop invite     # prints an invite, waits

# device B
./ephdrop -dir /tmp/b -name Phone join "ephdrop://pair/..."

# back on A: answer y to the prompt
./ephdrop -dir /tmp/a peers
./ephdrop -dir /tmp/a unpair <start of id>
```

Share and download files (A and B paired as above):

```
# on A: share files, keep running
./ephdrop -dir /tmp/a serve photo.jpg notes.txt

# on B: find A on the network, list its files, pull one
./ephdrop -dir /tmp/b list <start of A's id>
./ephdrop -dir /tmp/b get <start of A's id> <file id>
# if automatic finding does not work, add: -addr host:port
```

`serve` shares files for 24 hours unless you pass `-ttl` (at most 168h), and deletes them when they expire. Run only one `ephdrop` process per data directory at a time: the paired devices and the shared files are read when it starts.

Other commands: `id` shows this device's id and public key. Without `-dir`, data goes in your user config directory under `ephdrop/` (`identity.key` and `peers.json`, owner-only permissions).
