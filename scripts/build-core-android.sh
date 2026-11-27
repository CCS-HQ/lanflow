#!/usr/bin/env bash
# build-core-android.sh - build Mihomo for the Android app, from verified source.
#
# Usage:
#   scripts/build-core-android.sh
#
# Writes:
#   internal/platform/tunnel/android/kotlin/src/main/jniLibs/arm64-v8a/libmihomo.so
#
# Why not the official android-arm64 release: on Android, Mihomo's TUN reads
# the system's package list (/data/system/packages.xml) to map package names
# to user ids, and fails without it; only root may read that file, so in an
# ordinary app the TUN never starts. The build here applies one change, to
# a copy of the source: the package list is read only when the config names
# packages (tun.include-package / exclude-package / include-android-user),
# which Lanflow never does; the VPN itself picks the apps (VpnService).
# Everything else is the release's source, built as the release is
# (-tags with_gvisor, -trimpath, the version in constant.Version).
#
# The source is Go's module github.com/metacubex/mihomo at MIHOMO_VERSION,
# which the go command checks against the checksum database
# (sum.golang.org), and against the h1: sum pinned below for the pinned
# version: a mismatch stops the script. Its dependencies are checked against
# Mihomo's own go.sum. The patched file must match what the patch expects,
# or the script stops.
#
# Needs Go and the Android NDK (ANDROID_NDK_HOME, or the newest under
# $ANDROID_HOME/ndk, ~/Library/Android/sdk/ndk on macOS).
#
# Android runs programs only from an app's APK or its native library
# directory, hence the name lib*.so: the VPN runs it from inside the APK with
# the system's linker (internal/platform/tunnel/tunnel_android.go).
#
# Environment overrides:
#   MIHOMO_VERSION   tag to build (default: the pinned version below; any
#                    other is checked against the checksum database only)
#   ANDROID_NDK_HOME the NDK
set -euo pipefail

PINNED_VERSION="v1.19.34"
PINNED_H1="h1:+nkzw4xRehvk2ldLHMelXszd0ES1ysE/ArL92KPfQno="
MIHOMO_VERSION="${MIHOMO_VERSION:-$PINNED_VERSION}"
MIN_SDK=29 # mygo.json android.minSdk

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$REPO_ROOT/internal/platform/tunnel/android/kotlin/src/main/jniLibs/arm64-v8a/libmihomo.so"

log() { printf '%s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

command -v go >/dev/null 2>&1 || die "go is needed"

ndk="${ANDROID_NDK_HOME:-}"
if [ -z "$ndk" ]; then
	sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
	ndk="$(ls -d "$sdk"/ndk/*/ 2>/dev/null | sort -V | tail -n 1)"
	ndk="${ndk%/}"
fi
[ -n "$ndk" ] && [ -d "$ndk" ] || die "no Android NDK: set ANDROID_NDK_HOME"
cc="$(ls "$ndk"/toolchains/llvm/prebuilt/*/bin/aarch64-linux-android${MIN_SDK}-clang 2>/dev/null | head -n 1)"
[ -x "$cc" ] || die "the NDK in $ndk has no aarch64-linux-android${MIN_SDK}-clang"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/lanflow-build-core.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

# --- source ---------------------------------------------------------------
# Downloaded outside this repository's module, so that go.mod here stays as
# it is; GOFLAGS=-mod=mod lets go mod download run there.
log "downloading github.com/metacubex/mihomo@$MIHOMO_VERSION (checked against sum.golang.org)"
json="$(cd "$TMP" && GOFLAGS=-mod=mod GONOSUMDB= GOPRIVATE= GOINSECURE= go mod download -json "github.com/metacubex/mihomo@$MIHOMO_VERSION")" ||
	die "go mod download failed"
field() { printf '%s\n' "$json" | sed -n "s/^[[:space:]]*\"$1\": \"\(.*\)\",\{0,1\}$/\1/p" | head -n 1; }
src="$(field Dir)"
sum="$(field Sum)"
[ -d "$src" ] || die "no source directory in the go mod download answer"
if [ "$MIHOMO_VERSION" = "$PINNED_VERSION" ]; then
	[ "$sum" = "$PINNED_H1" ] || die "github.com/metacubex/mihomo@$MIHOMO_VERSION is $sum, expected $PINNED_H1"
fi
log "source: $src ($sum)"

# --- patch ----------------------------------------------------------------
# On a copy: the module cache is read-only, and go build -overlay refuses
# to replace files there.
cp -R "$src" "$TMP/src"
chmod -R u+w "$TMP/src"
target="$TMP/src/listener/sing_tun/server_android.go"
patched="$TMP/server_android.go"
grep -qF 'func (l *Listener) buildAndroidRules(tunOptions *tun.Options) error {' "$target" ||
	die "$target is not what the patch expects; review the change for $MIHOMO_VERSION"
awk '
	/^func \(l \*Listener\) buildAndroidRules\(tunOptions \*tun.Options\) error \{$/ {
		print
		print "\t// Lanflow: the package list is only readable by root; read it only"
		print "\t// for a config that names packages (scripts/build-core-android.sh)."
		print "\tif len(tunOptions.IncludePackage) == 0 && len(tunOptions.ExcludePackage) == 0 && len(tunOptions.IncludeAndroidUser) == 0 {"
		print "\t\treturn nil"
		print "\t}"
		done = 1
		next
	}
	{ print }
	END { if (!done) exit 1 }
' "$target" >"$patched" || die "patching $target failed"
mv -f "$patched" "$target"

# --- build ----------------------------------------------------------------
log "building Mihomo $MIHOMO_VERSION for android/arm64 with $cc"
(
	cd "$TMP/src"
	CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$cc" GOFLAGS=-mod=readonly \
		go build -tags with_gvisor -trimpath \
		-ldflags "-X github.com/metacubex/mihomo/constant.Version=$MIHOMO_VERSION -X github.com/metacubex/mihomo/constant.BuildTime=lanflow -w -s -buildid=" \
		-o "$TMP/mihomo" .
) || die "go build failed"

# An ELF for ARM64: the 64-bit class (byte 5 = 2) and machine 0xB7 (183).
head -c 20 "$TMP/mihomo" | od -An -tu1 | tr -s ' \n' ' ' | grep -q '^ 127 69 76 70 2 .* 183 0 *$' ||
	die "the build is not an ARM64 ELF program"
mkdir -p "$(dirname "$OUT")"
cp -f "$TMP/mihomo" "$OUT"
chmod 0755 "$OUT"

# Mihomo is GPL-3.0: its license goes into the app with it.
mkdir -p "$REPO_ROOT/bin/licenses/mihomo"
cp -f "$src/LICENSE" "$REPO_ROOT/bin/licenses/mihomo/LICENSE"

if command -v sha256sum >/dev/null 2>&1; then h="$(sha256sum "$OUT" | awk '{print $1}')"; else h="$(shasum -a 256 "$OUT" | awk '{print $1}')"; fi
log "installed: ${OUT#"$REPO_ROOT"/} (Mihomo $MIHOMO_VERSION, sha256 $h)"
