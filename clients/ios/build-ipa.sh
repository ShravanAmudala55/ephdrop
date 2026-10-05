#!/bin/sh
# Makes an unsigned ephdrop.ipa that SideStore can sign and install.
# Run build-lib.sh and xcodegen first.
set -e
cd "$(dirname "$0")"
rm -rf build
xcodebuild -project ephdrop.xcodeproj -scheme ephdrop -configuration Release \
  -sdk iphoneos -derivedDataPath build \
  CODE_SIGNING_ALLOWED=NO CODE_SIGN_IDENTITY="" build
mkdir -p build/Payload
cp -R build/Build/Products/Release-iphoneos/ephdrop.app build/Payload/
(cd build && zip -qry ephdrop.ipa Payload)
echo "Made $(pwd)/build/ephdrop.ipa. Open it with SideStore."
