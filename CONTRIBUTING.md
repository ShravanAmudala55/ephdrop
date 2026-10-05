# Contributing to ephdrop

Thank you for wanting to help. ephdrop is small, so a single person can make a real difference here.

## Ways to help

You do not need to write code.

- **Try it and tell us what happened.** Pair two devices, share a file, and open an issue with what worked and what did not. Real-device reports are the most useful thing right now.
- **Test on a platform we have not.** Linux desktops, Android phones and iPhones especially. See [what has been tried](README.md#where-it-works).
- **Improve the docs.** If a step confused you, it will confuse others. Fix the words.
- **Fix a bug or build a feature.** The list below is a good place to start.

## Where help is most useful

These are real gaps, roughly easiest first.

| Area | What | Good for |
|------|------|----------|
| Docs | Add a troubleshooting page (client isolation on routers, VPNs, firewalls) | Anyone |
| Linux | Try the AppImage and `.deb` on your distro and report what breaks | Linux users |
| Desktop | Scan a pairing QR code on a computer with the webcam (only paste works today) | JavaScript |
| Core | Resume an interrupted download instead of starting again | Go |
| Core | Let the user type an address by hand when devices cannot find each other | Go and JavaScript |
| Desktop | Test and fix the Mac `.dmg` and Windows `.exe` installers (`npm run dist`) | Mac or Windows users |
| Android | Build the app, fix whatever Gradle or Kotlin complains about, try it on a phone | Android developers |
| iPhone | Build the app, fix whatever Xcode complains about, try it with SideStore | iOS developers |
| iPhone | A Share extension so ephdrop shows up in the Share sheet | Swift |
| Accessibility | Keyboard use and screen reader labels in the window | Anyone |
| Translations | Make the window text translatable and add a language | Anyone |

If you want to work on one, say so in an issue first so two people do not do the same thing.

## Set up

You need [Go](https://go.dev/dl/) 1.24 or newer, and [Node.js](https://nodejs.org) 22 or newer for the desktop app.

```
git clone https://github.com/ShravanAmudala55/ephdrop
cd ephdrop

# the Go core
cd core
go test -race ./...

# the desktop app
cd ../desktop
npm install
npm start
```

The window you see is a web page in `core/api/ui` (plain HTML, CSS and JavaScript, no build step). You can change it and refresh to see the result.

The phone apps have their own steps: [Android](clients/android/README.md) and [iPhone](clients/ios/README.md).

## How the code is laid out

Read [docs/design.md](docs/design.md) first. The short version:

```
core/             Go: identity, pairing, discovery, shelf (files), transfer, board (file list), node, api
core/mobile/      The small Go interface the phone apps use
desktop/          Electron app for Mac, Windows and Linux
clients/android/  Android app (Kotlin)
clients/ios/      iPhone and iPad app (SwiftUI)
```

## What we care about

ephdrop has a few rules that decide what fits:

- **No server, no cloud, no account.** Nothing goes through the internet.
- **No tracking.** No analytics, no telemetry, no crash reports sent anywhere.
- **Files expire.** Short lived files are the point.
- **Small and understandable.** A new dependency needs a good reason. Prefer plain code over clever code.
- **Safe by default.** Anything that touches pairing, keys, the local API or file paths gets extra care and a test.

## Making a change

1. Fork the repository and make a branch.
2. Make the change. Keep it focused: one thing per pull request.
3. Add or update a test. Go tests live next to the code. For the window, `desktop/test/smoke.js` runs in a real Electron window.
4. Run the checks:
   ```
   cd core && gofmt -l . && go vet ./... && go test -race ./...
   ```
5. Open a pull request and fill in the template. Say how you checked it and which devices you tried.

Small pull requests get reviewed faster. If you are not sure about a big change, open an issue to talk about it first.

## Style

- Go: `gofmt`, and comments in plain English that say why, not what.
- JavaScript, Kotlin and Swift: follow the style of the file you are in.
- Words in the app and docs: plain, short, friendly. Write for someone who has never heard of mDNS.
- Commit messages: a short summary line in the present tense, such as "Resume interrupted downloads".

## Questions

Open a [discussion](https://github.com/ShravanAmudala55/ephdrop/discussions) or an issue. No question is too basic.

By taking part you agree to the [code of conduct](CODE_OF_CONDUCT.md). Contributions are under the project's [MIT licence](LICENSE).
