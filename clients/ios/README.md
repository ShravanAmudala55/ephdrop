# ephdrop for iPhone and iPad

A thin SwiftUI app around the shared Go program in `core/mobile`.

- The Go program does the real work (pairing, sharing, transfers).
- The window is the same page as the desktop app, shown in a web view.
- The app does the few things only iOS can: announce and find devices with Bonjour, scan
  a QR code with the camera, and take files from "Open in" and Share. Fetched files are
  saved in the app's Documents folder, which the Files app shows under
  On My iPhone, ephdrop.
- iPhones do not let an app keep sharing in the background. ephdrop runs while it is open
  and stops when you leave it. Files you shared from the phone can only be fetched by your
  other devices while the app is open.

## Build and run

You need Xcode, Go, and XcodeGen (`brew install xcodegen`).

1. From the repository folder:

   ```
   sh clients/ios/build-lib.sh
   ```

   This builds the Go program into `clients/ios/Mobile.xcframework`. It also adds
   `golang.org/x/mobile` to `core/go.mod` and `core/go.sum`, which is expected.
2. Make the Xcode project:

   ```
   cd clients/ios && xcodegen
   ```
3. Open `ephdrop.xcodeproj`. Under Signing & Capabilities pick your Apple ID as the team
   (a free account works). Plug in the iPhone, choose it, and press Run.
4. On the first run, allow Local Network and Camera when asked. Without Local Network
   the phone cannot see your other devices.

## Install with SideStore

```
sh clients/ios/build-ipa.sh
```

This makes `clients/ios/build/ephdrop.ipa`, unsigned. Open it with SideStore.

## What is not done yet

- Not tested on a real iPhone yet.
- No Share extension (the Share sheet entry that appears for any file). Files sent with
  "Open in ephdrop" work. A real extension needs a second app target and is planned.
- Files sent from other apps with "Open in ephdrop" are kept for one day. Files added in the window use the "Keep for" choice.
