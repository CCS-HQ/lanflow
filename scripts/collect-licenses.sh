#!/usr/bin/env bash
# Writes the license material that ships inside the app for one platform:
#   <dest>/README.txt                 what is in this folder
#   <dest>/THIRD-PARTY-NOTICES.md     the list of third-party components
#   <dest>/lanflow/LICENSE            Lanflow's own license (MIT)
#   <dest>/mihomo/LICENSE             Mihomo's license (GPL-3.0), as fetched and
#                                     verified by fetch-core.sh
#   <dest>/mihomo/SOURCE.txt          where the source of exactly this version is
#   <dest>/geodata/NOTICE.txt         origin and attribution of the geo databases,
#                                     with the SHA-256 of the files packed
#   <dest>/go/go/                     the Go distribution's LICENSE and PATENTS
#   <dest>/go/<module>@<version>/...  the license files of every Go module
#                                     linked into the app or the helper for that
#                                     platform, copied from the module cache
# Nothing here is written by hand: the texts are the upstream files.
# Usage: scripts/collect-licenses.sh <goos> <goarch> <dest>
#   <dest> must be a directory named "licenses"; it is replaced.
# Called by prepare-resources.sh after fetch-core.sh; downloads nothing.
set -euo pipefail
cd "$(dirname "$0")/.."

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ "$#" -eq 3 ] || die "usage: scripts/collect-licenses.sh <goos> <goarch> <dest>"
GOOS_T="$1"; GOARCH_T="$2"; DEST="$3"
[ "$(basename "$DEST")" = licenses ] || die "<dest> must be a directory named licenses, got $DEST"

PINNED_VERSION="$(sed -n 's/^PINNED_VERSION="\([^"]*\)".*/\1/p' scripts/fetch-core.sh)"
MIHOMO_VERSION="${MIHOMO_VERSION:-$PINNED_VERSION}"
[ -n "$MIHOMO_VERSION" ] || die "cannot read PINNED_VERSION from scripts/fetch-core.sh"
MIHOMO_LICENSE="bin/licenses/mihomo/LICENSE"
[ -s "$MIHOMO_LICENSE" ] || die "$MIHOMO_LICENSE missing; run scripts/fetch-core.sh $GOOS_T $GOARCH_T first"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

rm -rf "$DEST"
mkdir -p "$DEST/lanflow" "$DEST/mihomo" "$DEST/geodata" "$DEST/go/go"

cp LICENSE "$DEST/lanflow/LICENSE"
cp THIRD-PARTY-NOTICES.md "$DEST/THIRD-PARTY-NOTICES.md"

# --- Mihomo ---------------------------------------------------------------
cp "$MIHOMO_LICENSE" "$DEST/mihomo/LICENSE"
cat > "$DEST/mihomo/SOURCE.txt" <<EOF
Mihomo ${MIHOMO_VERSION}
https://github.com/MetaCubeX/mihomo

Lanflow ships the official release build of Mihomo ${MIHOMO_VERSION} as a
separate program (mihomo, or mihomo.exe on Windows) and runs it as a child
process. The program is not modified; on macOS the packaging may add an
ad hoc code signature to it.

Mihomo is licensed under the GNU General Public License, version 3. The
license text is the file LICENSE next to this one.

The complete corresponding source code of exactly this version:
  https://github.com/MetaCubeX/mihomo/tree/${MIHOMO_VERSION}
  https://github.com/MetaCubeX/mihomo/archive/refs/tags/${MIHOMO_VERSION}.tar.gz
The release this binary was taken from:
  https://github.com/MetaCubeX/mihomo/releases/tag/${MIHOMO_VERSION}

Other Mihomo versions installed later from inside Lanflow (Settings > Core)
are downloaded from the same repository; the source of each is at the tag
of the same name.

Mihomo ${MIHOMO_VERSION}
Lanflow 随包分发 Mihomo ${MIHOMO_VERSION} 的官方发布版，作为独立程序以子进程方式运行，
未作修改（macOS 打包时可能为它附加 ad hoc 代码签名）。Mihomo 以 GNU 通用公共许可证
第 3 版（GPL-3.0）发布，许可证全文见同目录的 LICENSE。与该版本完全对应的源代码见上面的
链接。之后在澜流内安装的其他 Mihomo 版本来自同一仓库，源代码在同名 tag。
EOF

# --- geodata --------------------------------------------------------------
{
  cat <<'EOF'
Geo databases shipped with Lanflow (the folder geo/)

  geoip.metadb        IP to country and category
  geosite.dat         domain categories
  GeoLite2-ASN.mmdb   IP to autonomous system

All three are downloaded, unmodified, from the "latest" release of
https://github.com/MetaCubeX/meta-rules-dat (GPL-3.0). That repository
compiles them from the projects named in its README, among them
Loyalsoldier/geoip, v2fly/domain-list-community and
Loyalsoldier/domain-list-custom; see there for the full list and the
license of each source.

GeoLite2-ASN.mmdb, and the MaxMind data that the IP databases build on:
  This product includes GeoLite2 Data created by MaxMind, available from
  https://www.maxmind.com.

The map's land outline is made with Natural Earth (public domain),
https://www.naturalearthdata.com.

SHA-256 of the files packed with this build:
EOF
  for f in geoip.metadb geosite.dat GeoLite2-ASN.mmdb; do
    if [ -f "resources/geo/$f" ]; then
      printf '  %s  %s\n' "$(sha256 "resources/geo/$f")" "$f"
    else
      printf '  (not packed)  %s\n' "$f"
    fi
  done
} > "$DEST/geodata/NOTICE.txt"

# --- Go ---------------------------------------------------------------------
# A license file is one of these names, in any case, with any extension.
is_license_name() {
  local n
  n="$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')"
  case "$n" in
  LICENSE* | LICENCE* | UNLICENSE* | COPYING* | NOTICE* | PATENTS*) return 0 ;;
  esac
  return 1
}

# copy_licenses DIR DEST: the license files directly in DIR.
copy_licenses() {
  local f n
  for f in "$1"/*; do
    [ -f "$f" ] || continue
    n="$(basename "$f")"
    is_license_name "$n" || continue
    [ -e "$2/$n" ] && continue
    mkdir -p "$2"
    cp "$f" "$2/$n"
    chmod u+w "$2/$n" # the module cache is read-only
  done
}

# The Go distribution (standard library and runtime, linked into both
# programs).
GOROOT_DIR="$(go env GOROOT)"
GOROOT_DIR="${GOROOT_DIR//\\//}"
copy_licenses "$GOROOT_DIR" "$DEST/go/go"
[ -f "$DEST/go/go/LICENSE" ] || die "no LICENSE in GOROOT ($GOROOT_DIR)"
go version > "$DEST/go/go/VERSION.txt"

# Every package of another module that the app or the helper links for the
# target: the license files of its directory and of each directory above it,
# up to the module's root (a module may carry code under another license in
# a subdirectory, with the license beside it). A module without a license
# file in its root stops the script.
modules=0
while IFS=$'\t' read -r mod moddir pkgdir; do
  [ -n "$mod" ] || continue
  moddir="${moddir//\\//}"
  pkgdir="${pkgdir//\\//}"
  out="$DEST/go/$mod"
  if [ ! -d "$out" ]; then
    copy_licenses "$moddir" "$out"
    [ -d "$out" ] || die "module $mod has no license file in $moddir"
    modules=$((modules + 1))
  fi
  d="$pkgdir"
  while [ "$d" != "$moddir" ] && [ "${d#"$moddir"/}" != "$d" ]; do
    copy_licenses "$d" "$out/${d#"$moddir"/}"
    d="$(dirname "$d")"
  done
done < <(GOOS="$GOOS_T" GOARCH="$GOARCH_T" CGO_ENABLED=0 go list -deps \
  -f '{{with .Module}}{{if .Version}}{{.Path}}@{{.Version}}{{"\t"}}{{.Dir}}{{"\t"}}{{$.Dir}}{{end}}{{end}}' \
  . ./cmd/lanflow-helper | sort -u)
[ "$modules" -gt 0 ] || die "go list reported no modules for $GOOS_T/$GOARCH_T"

cat > "$DEST/README.txt" <<EOF
Licenses of Lanflow and of what ships with it (${GOOS_T}/${GOARCH_T}).

  THIRD-PARTY-NOTICES.md   the list of third-party components
  lanflow/                 Lanflow itself (MIT)
  mihomo/                  the Mihomo core (GPL-3.0): license text, and where
                           the source of this version is (SOURCE.txt)
  geodata/                 origin and attribution of the geo databases
  go/                      the Go distribution and the Go modules linked into
                           Lanflow and its helper, each under its module path
                           and version

澜流及其随包组件的许可证。Mihomo 内核为 GPL-3.0，许可证全文与对应版本源代码的位置见 mihomo/。
EOF

echo "licenses for ${GOOS_T}/${GOARCH_T}: Mihomo ${MIHOMO_VERSION}, ${modules} Go modules -> $DEST"
