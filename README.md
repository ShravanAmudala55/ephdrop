# ephdrop

Ephemeral local file drop. Drop a file on one device, pull it on another, and it deletes itself after a day or two.

Status: planning (see [TRACKING.md](TRACKING.md)).

## The idea

Households mix iPhone, Android, Windows and Mac, and sharing files between them is painful. ephdrop is a small open source app for all four platforms:

- No server and no cloud. Devices find each other on the home Wi-Fi and transfer files directly.
- No account. Devices pair once with a QR code.
- Files expire. Every file carries an expiry time (default 1 or 2 days) and each device deletes it on its own.
- Tap to pull. Devices share a small list of what is available. A file is only downloaded when you tap it.

## Platform behaviour

| Platform | Role |
|----------|------|
| Windows | Background peer, stays in sync, hosts files |
| Mac | Background peer, stays in sync, hosts files |
| Android | Background peer (needs battery optimisation off) |
| iOS | Foreground client: open the app, see the list, tap to pull. Distributed as an .ipa for SideStore |

## Not goals

- Long term sync or backup (use Syncthing or Resilio for that)
- Sharing over the internet
- Version history or conflict resolution

## Layout

```
core/            Go library: discovery, pairing, file list, transfer, expiry
clients/windows  Windows app
clients/mac      Mac app
clients/android  Android app
clients/ios      iOS app (SwiftUI)
docs/            Design notes
```

## Docs

- [Design](docs/design.md)
- [Tracking](TRACKING.md)
