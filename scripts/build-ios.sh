#!/usr/bin/env bash
# Builds Lanflow for iOS with its packet tunnel extension. MyGo builds the
# app alone; this script then builds the extension (Mihomo as a Go
# c-archive, mobile/ios/tunnelcore, linked into the Swift of
# mobile/ios/PacketTunnel), puts it in the app as PlugIns/PacketTunnel.appex
# and signs the extension, then the app around it.
#
# Usage:
#   scripts/build-ios.sh simulator   build/ios-simulator/Lanflow.app, signed ad hoc
#                                    (a simulator runs no packet tunnel: this
#                                    checks the build and the bundle only)
#   scripts/build-ios.sh device      build/ios-arm64/<slug>-<version>.ipa
#
# A device build signs with the profiles of a paid developer account with the
# Network Extensions and App Groups capabilities (docs/phone-ios.md):
#   - the app's: picked by mygo (mygo.json ios.provisioningProfile / team);
#   - the extension's, for <identifier>.PacketTunnel:
#       LANFLOW_IOS_EXT_PROFILE=/path/to/PacketTunnel.mobileprovision
#   - the identity defaults to the one mygo signed the app with;
#       LANFLOW_IOS_IDENTITY overrides it (a SHA-1 or a name).
# LANFLOW_IOS_SKIP_APP=1 reuses the app mygo built last.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$PWD"

MODE="${1:-simulator}"
case "$MODE" in
simulator) SDK=iphonesimulator PLATFORM=iPhoneSimulator OUT=build/ios-simulator ;;
device) SDK=iphoneos PLATFORM=iPhoneOS OUT=build/ios-arm64 ;;
*) echo "usage: $0 simulator|device" >&2; exit 2 ;;
esac

json() { # json <key path as python-ish dotted path> : read mygo.json without jq
	plutil -extract "$1" raw -o - mygo.json 2>/dev/null || true
}
IDENTIFIER="$(json identifier)"
VERSION="$(json version)"
MIN_OS="$(json ios.minimumSystemVersion)"; MIN_OS="${MIN_OS:-17.0}"
BUILD_NUMBER="$(json ios.buildNumber)"; BUILD_NUMBER="${BUILD_NUMBER:-$VERSION}"
APP_GROUP="$(json ios.infoPlist.LanflowAppGroup)"
[ -n "$APP_GROUP" ] || { echo "error: mygo.json ios.infoPlist.LanflowAppGroup missing" >&2; exit 1; }
EXT_ID="$IDENTIFIER.PacketTunnel"
CORE_VERSION="$(sed -n 's/^const CoreVersion = "\(.*\)"$/\1/p' internal/platform/tunnel/iosvpn/iosvpn.go)"

SDK_PATH="$(xcrun --sdk "$SDK" --show-sdk-path)"
CLANG="$(xcrun --sdk "$SDK" -f clang)"
if [ "$MODE" = simulator ]; then TARGET="arm64-apple-ios${MIN_OS}-simulator"; else TARGET="arm64-apple-ios${MIN_OS}"; fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/lanflow-ios.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# --- 1. the app -------------------------------------------------------------
if [ "${LANFLOW_IOS_SKIP_APP:-}" != 1 ]; then
	if [ "$MODE" = simulator ]; then
		go tool mygo build -platform ios/simulator
	else
		go tool mygo build -platform ios
	fi
fi
if [ "$MODE" = simulator ]; then
	APP="$OUT/Lanflow.app"
	[ -d "$APP" ] || { echo "error: $APP missing" >&2; exit 1; }
else
	IPA="$(ls -t "$OUT"/*.ipa 2>/dev/null | head -1)"
	[ -n "$IPA" ] || { echo "error: no .ipa in $OUT" >&2; exit 1; }
	ditto -x -k "$IPA" "$WORK/ipa"
	APP="$(ls -d "$WORK"/ipa/Payload/*.app | head -1)"
fi

# --- 2. Mihomo as a library -------------------------------------------------
# Tags: gVisor for the TUN's mixed stack; low memory buffers for the
# extension's 50 MB; no Tailscale, ZeroTier or EasyTier outbounds, whose
# userspace networks alone overrun it.
echo "==> tunnelcore ($TARGET, Mihomo $CORE_VERSION)"
(
	cd mobile/ios/tunnelcore
	GOOS=ios GOARCH=arm64 CGO_ENABLED=1 \
		CC="$CLANG -isysroot $SDK_PATH -target $TARGET" \
		go build -trimpath -buildmode=c-archive \
		-tags with_gvisor,with_low_memory,no_tailscale,no_zerotier,no_easytier \
		-ldflags "-s -w -X github.com/metacubex/mihomo/constant.Version=$CORE_VERSION -X github.com/metacubex/mihomo/constant.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		-o "$WORK/libtunnelcore.a" .
)

# --- 3. the extension -------------------------------------------------------
echo "==> PacketTunnel.appex"
APPEX="$WORK/PacketTunnel.appex"
mkdir -p "$APPEX"
EXT_SRC=mobile/ios/PacketTunnel
subst() {
	sed -e "s|\$(IDENTIFIER)|$IDENTIFIER|g" -e "s|\$(VERSION)|$VERSION|g" -e "s|\$(BUILD)|$BUILD_NUMBER|g" \
		-e "s|\$(PLATFORM)|$PLATFORM|g" -e "s|\$(MIN_OS)|$MIN_OS|g" -e "s|\$(PRODUCT_MODULE_NAME)|PacketTunnel|g" \
		-e "s|group\.$IDENTIFIER|$APP_GROUP|g" "$1"
}
subst "$EXT_SRC/Info.plist" >"$APPEX/Info.plist"
plutil -lint -s "$APPEX/Info.plist"
subst "$EXT_SRC/PacketTunnel.entitlements" >"$WORK/PacketTunnel.entitlements"

LINK_EXTRA=()
if [ "$MODE" = simulator ]; then
	# A simulator takes entitlements from the executable, as MyGo does for
	# the app (an ad hoc signature carries none).
	LINK_EXTRA=(-Xlinker -sectcreate -Xlinker __TEXT -Xlinker __entitlements -Xlinker "$WORK/PacketTunnel.entitlements")
fi
xcrun --sdk "$SDK" swiftc -target "$TARGET" -sdk "$SDK_PATH" \
	-module-name PacketTunnel -parse-as-library -application-extension -O -wmo \
	-import-objc-header "$EXT_SRC/Bridge.h" -I "$WORK" \
	"$EXT_SRC/PacketTunnelProvider.swift" "$WORK/libtunnelcore.a" \
	-o "$APPEX/PacketTunnel" \
	-Xlinker -e -Xlinker _NSExtensionMain -Xlinker -dead_strip \
	-framework NetworkExtension -framework Foundation -framework Security \
	-framework CoreFoundation -framework SystemConfiguration -lresolv \
	${LINK_EXTRA[@]+"${LINK_EXTRA[@]}"}

rm -rf "$APP/PlugIns/PacketTunnel.appex"
mkdir -p "$APP/PlugIns"
cp -R "$APPEX" "$APP/PlugIns/"
APPEX="$APP/PlugIns/PacketTunnel.appex"

# --- 4. signing: the extension first, then the app around it ----------------
echo "==> signing"
if [ "$MODE" = simulator ]; then
	codesign --force --sign - "$APPEX"
	codesign --force --sign - "$APP"
else
	PROFILE="${LANFLOW_IOS_EXT_PROFILE:-}"
	[ -f "$PROFILE" ] || { echo "error: set LANFLOW_IOS_EXT_PROFILE to the provisioning profile of $EXT_ID (docs/phone-ios.md)" >&2; exit 1; }
	security cms -D -i "$PROFILE" >"$WORK/ext-profile.plist"
	pb() { /usr/libexec/PlistBuddy -c "$1" "$2" 2>/dev/null; }
	PREFIX="$(pb 'Print :ApplicationIdentifierPrefix:0' "$WORK/ext-profile.plist")"
	TEAM="$(pb 'Print :TeamIdentifier:0' "$WORK/ext-profile.plist")"
	PROFILE_APPID="$(pb 'Print :Entitlements:application-identifier' "$WORK/ext-profile.plist")"
	case "$PROFILE_APPID" in
	"$PREFIX.$EXT_ID" | "$PREFIX.*" | "$PREFIX.$IDENTIFIER.*") ;;
	*) echo "error: $PROFILE is for $PROFILE_APPID, not $PREFIX.$EXT_ID" >&2; exit 1 ;;
	esac
	pb 'Print :Entitlements:com.apple.developer.networking.networkextension' "$WORK/ext-profile.plist" | grep -q packet-tunnel-provider ||
		{ echo "error: $PROFILE lacks the Network Extensions capability (packet-tunnel-provider)" >&2; exit 1; }
	pb 'Print :Entitlements:com.apple.security.application-groups' "$WORK/ext-profile.plist" | grep -q "$APP_GROUP" ||
		{ echo "error: $PROFILE lacks the App Group $APP_GROUP" >&2; exit 1; }

	# The extension's entitlements: its own identity, the packet tunnel and
	# the app's group, as the profile allows.
	ENT="$WORK/ext.entitlements"
	cp "$WORK/PacketTunnel.entitlements" "$ENT"
	/usr/libexec/PlistBuddy -c "Add :application-identifier string $PREFIX.$EXT_ID" \
		-c "Add :com.apple.developer.team-identifier string $TEAM" "$ENT"
	if [ "$(pb 'Print :Entitlements:get-task-allow' "$WORK/ext-profile.plist")" = true ]; then
		/usr/libexec/PlistBuddy -c "Add :get-task-allow bool true" "$ENT"
	fi

	# The app's entitlements, as mygo signed it, with the Network
	# Extension types narrowed to the packet tunnel (a profile lists them
	# all; App Store validation takes only those the app uses).
	APP_ENT="$WORK/app.entitlements"
	codesign -d --entitlements "$APP_ENT" --xml "$APP" 2>/dev/null
	/usr/libexec/PlistBuddy -c "Delete :com.apple.developer.networking.networkextension" "$APP_ENT" 2>/dev/null || true
	/usr/libexec/PlistBuddy -c "Add :com.apple.developer.networking.networkextension array" \
		-c "Add :com.apple.developer.networking.networkextension:0 string packet-tunnel-provider" "$APP_ENT"
	pb 'Print :com.apple.security.application-groups' "$APP_ENT" | grep -q "$APP_GROUP" ||
		{ echo "error: the app's profile lacks the App Group $APP_GROUP" >&2; exit 1; }

	IDENTITY="${LANFLOW_IOS_IDENTITY:-}"
	if [ -z "$IDENTITY" ]; then
		IDENTITY="$(codesign -dvv "$APP" 2>&1 | sed -n 's/^Authority=//p' | head -1)"
	fi
	[ -n "$IDENTITY" ] || { echo "error: cannot tell the app's signing identity; set LANFLOW_IOS_IDENTITY" >&2; exit 1; }

	cp "$PROFILE" "$APPEX/embedded.mobileprovision"
	codesign --force --sign "$IDENTITY" --entitlements "$ENT" --generate-entitlement-der "$APPEX"
	codesign --force --sign "$IDENTITY" --entitlements "$APP_ENT" --generate-entitlement-der "$APP"

	IPA_OUT="$ROOT/$IPA"
	rm -f "$IPA_OUT"
	(cd "$WORK/ipa" && ditto -c -k --sequesterRsrc --keepParent Payload "$IPA_OUT")
fi

codesign --verify --deep --strict "$APP"
echo "==> signed:"
for b in "$APPEX" "$APP"; do
	echo "--- ${b#"$WORK/ipa/Payload/"}"
	codesign -dv --entitlements - --xml "$b" 2>&1 | grep -E '^(Identifier|Authority|TeamIdentifier|Signature)=' || true
done
if [ "$MODE" = simulator ]; then
	echo "built $APP (PlugIns/PacketTunnel.appex: $(du -sh "$APPEX" | cut -f1))"
else
	echo "built $IPA (PlugIns/PacketTunnel.appex: $(du -sh "$APPEX" | cut -f1))"
fi
