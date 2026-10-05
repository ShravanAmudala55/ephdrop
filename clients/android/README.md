# ephdrop for Android

A thin Kotlin app around the shared Go program in `core/mobile`.

- The Go program does the real work (pairing, sharing, transfers). It runs inside a
  foreground service, so other devices can reach this phone while the app is closed.
- The window is the same page as the desktop app, shown in a web view.
- The app does the few things only Android can: find the phone's Wi-Fi address, announce
  and find devices with Android's network service discovery, pick files, take files from
  the Share menu, scan a QR code, and save fetched files into Downloads/ephdrop.

## Build and run

You need Android Studio (with the SDK and NDK installed) and Go.

1. In Android Studio's SDK Manager, open the SDK Tools tab and tick **NDK (Side by side)**.
2. In a terminal, from the repository folder:

   ```
   sh clients/android/build-lib.sh
   ```

   This builds the Go program into `clients/android/app/libs/ephdrop.aar`. It also adds
   `golang.org/x/mobile` to `core/go.mod` and `core/go.sum`, which is expected.
3. In Android Studio choose **Open** and pick the `clients/android` folder. Let it sync.
   Accept it if it offers to update the Android Gradle plugin.
4. Plug in a phone with USB debugging on (or start an emulator), and press Run.

## Try it

1. Open the app on the phone and allow notifications. A notification "ephdrop is on" stays
   while it is sharing.
2. On the desktop, choose Add device and show the code. On the phone choose Add device,
   open "Enter a code", then Scan QR code. Accept on the desktop.
3. Add a file on either side. It should appear on the other within a few seconds.
4. On the phone, tap a file from another device, then Save. It lands in Downloads/ephdrop.
5. In any app on the phone, use Share and pick ephdrop to send a file to your devices.

## Keep it running in the background

Phone makers stop background apps to save battery. If a device stops seeing the phone:
Settings > Apps > ephdrop > Battery > Unrestricted (the wording differs by maker).

## What is not done yet

- Not tested on a real phone yet.
- The first scan needs Google Play services (the scanner comes from there).
- Pairing works with a code scan or paste. Finding devices relies on both being on the
  same Wi-Fi without client isolation.
