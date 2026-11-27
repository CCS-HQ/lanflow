#!/usr/bin/env bash
# build-android.sh - build the Android app (arm64 APK), with its core.
#
# Usage:
#   scripts/build-android.sh
#
# Writes:
#   build/android/lanflow-<version>-android-arm64.apk
#
# Steps: scripts/build-core-android.sh (Mihomo from verified source, into
# the plugin's jniLibs), then `go tool mygo build -platform android/arm64`
# (docs/phone-android.md), then apksigner prints whom the APK is signed by.
#
# Signing: without LANFLOW_ANDROID_KEYSTORE the APK is signed with the
# machine's debug key, which is fine to try the app but not to publish: a
# phone only installs an update signed by the key of the installed app, and
# every machine (every CI runner) has a debug key of its own. With it set,
# mygo.json gets android.keystore / keyAlias for this build only (restored
# afterwards); the passwords stay in the environment, as mygo reads them.
#
# Environment:
#   JAVA_HOME                       a JDK 17 (MyGo's Gradle project needs 17)
#   ANDROID_HOME, ANDROID_NDK_HOME  the SDK and NDK (defaults as mygo finds them)
#   LANFLOW_ANDROID_KEYSTORE        the upload keystore (.jks), optional
#   LANFLOW_ANDROID_KEY_ALIAS       the key's alias in it (default: lanflow)
#   MYGO_ANDROID_KEYSTORE_PASSWORD  the keystore's password (with a keystore)
#   MYGO_ANDROID_KEY_PASSWORD       the key's password (default: the keystore's)
#   LANFLOW_ANDROID_CERT_SHA256     when set, the signing certificate's
#                                   SHA-256 must be this (hex, any case,
#                                   colons allowed): a wrong key stops here
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

log() { printf '%s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

version="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' mygo.json)"
[ -n "$version" ] || die "no version in mygo.json"

if [ -n "${LANFLOW_ANDROID_KEYSTORE:-}" ]; then
	[ -s "$LANFLOW_ANDROID_KEYSTORE" ] || die "LANFLOW_ANDROID_KEYSTORE ($LANFLOW_ANDROID_KEYSTORE) is not a file"
	[ -n "${MYGO_ANDROID_KEYSTORE_PASSWORD:-}" ] || die "the keystore needs MYGO_ANDROID_KEYSTORE_PASSWORD"
	command -v jq >/dev/null 2>&1 || die "jq is needed to sign"
	keystore="$(cd "$(dirname "$LANFLOW_ANDROID_KEYSTORE")" && pwd)/$(basename "$LANFLOW_ANDROID_KEYSTORE")"
	cp mygo.json "$REPO_ROOT/.mygo.json.orig"
	trap 'mv -f "$REPO_ROOT/.mygo.json.orig" "$REPO_ROOT/mygo.json"' EXIT
	jq --arg ks "$keystore" --arg alias "${LANFLOW_ANDROID_KEY_ALIAS:-lanflow}" \
		'.android.keystore = $ks | .android.keyAlias = $alias' "$REPO_ROOT/.mygo.json.orig" >mygo.json
	log "signing with $keystore (alias ${LANFLOW_ANDROID_KEY_ALIAS:-lanflow})"
else
	log "no LANFLOW_ANDROID_KEYSTORE: signing with the debug key (not for publishing)"
fi

scripts/build-core-android.sh

rm -f "build/android/lanflow-$version.apk" "build/android/lanflow-$version.aab"
go tool mygo build -platform android/arm64
apk="build/android/lanflow-$version.apk"
[ -s "$apk" ] || die "mygo did not write $apk"

# The core must be inside, stored uncompressed (it is run from the APK).
files="$(unzip -l "$apk")" # whole, not piped: grep -q would end unzip with SIGPIPE
grep -q 'lib/arm64-v8a/libmihomo.so$' <<<"$files" || die "$apk has no lib/arm64-v8a/libmihomo.so"

out="build/android/lanflow-$version-android-arm64.apk"
mv -f "$apk" "$out"

# Who signed it: apksigner of the newest build-tools in the SDK.
sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
apksigner="$(ls -d "$sdk"/build-tools/*/ 2>/dev/null | sort -V | tail -n 1)apksigner"
if [ -x "$apksigner" ]; then
	certs="$("$apksigner" verify --print-certs "$out")" || die "apksigner rejects $out"
	printf '%s\n' "$certs" >&2
	if [ -n "${LANFLOW_ANDROID_CERT_SHA256:-}" ]; then
		want="$(printf '%s' "$LANFLOW_ANDROID_CERT_SHA256" | tr -d ':' | tr 'A-F' 'a-f')"
		got="$(printf '%s\n' "$certs" | sed -n 's/.*certificate SHA-256 digest: \([0-9a-f]*\).*/\1/p' | head -n 1)"
		[ "$got" = "$want" ] || die "$out is signed by certificate $got, expected $want"
	fi
elif [ -n "${LANFLOW_ANDROID_CERT_SHA256:-}" ]; then
	die "no apksigner in $sdk/build-tools to check the signing certificate"
fi

log "built: $out"
