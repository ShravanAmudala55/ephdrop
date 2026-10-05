#!/bin/sh
# Builds the Go program (core/mobile) into app/libs/ephdrop.aar for the Android app.
# Needs Go, and the Android SDK with an NDK (Android Studio: Settings > Languages &
# Frameworks > Android SDK > SDK Tools > NDK (Side by side)).
set -e
cd "$(dirname "$0")/../../core"

export PATH="$PATH:/usr/local/go/bin:/opt/homebrew/bin:$(go env GOPATH)/bin"
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Library/Android/sdk}"
if [ -z "$ANDROID_NDK_HOME" ]; then
  ANDROID_NDK_HOME="$(ls -d "$ANDROID_HOME"/ndk/* 2>/dev/null | tail -1)"
fi
if [ -z "$ANDROID_NDK_HOME" ]; then
  echo "No NDK found in $ANDROID_HOME/ndk. Install it from Android Studio's SDK Manager (SDK Tools tab)." >&2
  exit 1
fi
export ANDROID_NDK_HOME
echo "Using NDK: $ANDROID_NDK_HOME"

# If a vendor folder is lying around (used for offline builds), ignore it here.
export GOFLAGS=-mod=mod

go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
go get golang.org/x/mobile/bind
gomobile init

mkdir -p ../clients/android/app/libs
# arm64 covers real phones and the emulator on Apple silicon Macs.
gomobile bind -target=android/arm64 -androidapi 29 -javapkg dev.ephdrop \
  -o ../clients/android/app/libs/ephdrop.aar ./mobile

echo
echo "Built clients/android/app/libs/ephdrop.aar. Classes inside:"
unzip -p ../clients/android/app/libs/ephdrop.aar classes.jar > /tmp/ephdrop-classes.jar
unzip -l /tmp/ephdrop-classes.jar | grep -E "dev/ephdrop" || true
