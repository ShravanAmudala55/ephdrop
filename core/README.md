# core

Go library shared by all clients: identity, discovery, pairing, file list, transfer and expiry. See [../docs/design.md](../docs/design.md).

## Packages

| Package | Status | What it does |
|---------|--------|--------------|
| `identity` | done | ed25519 device key, device id, key storage, pinned TLS config |
| `cmd/ephdrop` | started | development command line driver |

## Try it

```
go test -race ./...
go run ./cmd/ephdrop id
```

`id` prints this device's id and public key, creating them on first run. The key is stored in your user config directory under `ephdrop/identity.key` with owner-only permissions. Use `-dir` to choose another folder.
