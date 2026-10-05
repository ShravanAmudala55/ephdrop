# core

Go library shared by all clients: identity, discovery, pairing, file list, transfer and expiry. See [../docs/design.md](../docs/design.md).

## Packages

| Package | Status | What it does |
|---------|--------|--------------|
| `identity` | done | ed25519 device key, device id, key storage, pinned TLS config |
| `pairing` | done | one time invites, paired device list, pairing handshake |
| `discovery` | logic done, mDNS backend pending | announcement format, Finder for visible paired devices, address hints |
| `internal/atomicfile` | done | crash safe file writes |
| `cmd/ephdrop` | in progress | development command line driver |

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

Other commands: `id` shows this device's id and public key. Without `-dir`, data goes in your user config directory under `ephdrop/` (`identity.key` and `peers.json`, owner-only permissions).
