#!/bin/sh
# Builds the Go program (core/mobile) into Mobile.xcframework for the iPhone app.
# Needs Go and Xcode (with its command line tools).
set -e
cd "$(dirname "$0")/../../core"

export PATH="$PATH:/usr/local/go/bin:/opt/homebrew/bin:$(go env GOPATH)/bin"
# If a vendor folder is lying around (used for offline builds), ignore it here.
export GOFLAGS=-mod=mod

go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
go get golang.org/x/mobile/bind
gomobile init

rm -rf ../clients/ios/Mobile.xcframework
# Real iPhones and the simulator.
gomobile bind -target=ios -o ../clients/ios/Mobile.xcframework ./mobile

echo
echo "Built clients/ios/Mobile.xcframework."
echo "Names Swift will see (check them if Xcode complains):"
grep -rhoE "(MobileStart|MobileAgent|MobileNativeProtocol|MobileNative)\b" ../clients/ios/Mobile.xcframework --include=*.h | sort | uniq -c
