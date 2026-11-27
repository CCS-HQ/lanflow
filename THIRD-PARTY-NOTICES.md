# 第三方组件声明 / Third-Party Notices

澜流 Lanflow 本身以 [MIT 许可证](LICENSE) 发布。安装包里还带有下列第三方组件，各自遵循自己的许可证。

每个安装包内的 `licenses/` 文件夹（macOS 在 `Lanflow.app/Contents/Resources/licenses/`，Windows 在安装目录的 `licenses\`）有这些许可证的全文，由 `scripts/collect-licenses.sh` 在打包时从上游文件生成。

Lanflow itself is released under the MIT License. The packages also contain the third-party components below, each under its own license. The full license texts are in the `licenses/` folder inside every package.

## Mihomo 内核（随包分发的独立程序）

| 项目 | 内容 |
| --- | --- |
| 组件 | [Mihomo](https://github.com/MetaCubeX/mihomo) v1.19.34，官方发布版，未作修改，以子进程方式运行 |
| 许可证 | GNU General Public License v3.0（全文见包内 `licenses/mihomo/LICENSE`，或 <https://github.com/MetaCubeX/mihomo/blob/v1.19.34/LICENSE>） |
| 对应源代码 | <https://github.com/MetaCubeX/mihomo/tree/v1.19.34>（压缩包：<https://github.com/MetaCubeX/mihomo/archive/refs/tags/v1.19.34.tar.gz>） |

Mihomo v1.19.34 is distributed with Lanflow as a separate, unmodified program under the GNU General Public License, version 3. The complete corresponding source code of that version is at the link above.

在澜流内（设置 › 内核）另行下载的其他 Mihomo 版本来自同一仓库，源代码在同名 tag。

## 地理数据库（随包分发的数据文件）

| 文件 | 来源 |
| --- | --- |
| `geoip.metadb`、`geosite.dat`、`GeoLite2-ASN.mmdb` | [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat) 的 `latest` 发布（GPL-3.0），原样分发。上游数据来源见该仓库 README，其中包括 [Loyalsoldier/geoip](https://github.com/Loyalsoldier/geoip)、[v2fly/domain-list-community](https://github.com/v2fly/domain-list-community)、[Loyalsoldier/domain-list-custom](https://github.com/Loyalsoldier/domain-list-custom) |

This product includes GeoLite2 Data created by MaxMind, available from <https://www.maxmind.com>.

每个版本实际打包的三个文件的 SHA-256 记录在该版本的 Release 说明、Release 附件 `lanflow-<tag>-geodata.sha256` 和包内 `licenses/geodata/NOTICE.txt` 中。

## 地图数据（编入程序）

陆地点阵由 [Natural Earth](https://www.naturalearthdata.com) 1:110m 陆地多边形生成（公有领域）。Made with Natural Earth.

## Go 模块（编入澜流与权限组件）

版本以 [`go.mod`](go.mod) 为准；每个发布版另附 SPDX 格式的 SBOM（`lanflow-<tag>.spdx.json`）。

| 模块 | 许可证 |
| --- | --- |
| [Go 标准库与运行时](https://go.dev) | BSD-3-Clause |
| [github.com/andybalholm/brotli](https://github.com/andybalholm/brotli) | MIT |
| [github.com/egoist/mygo](https://github.com/egoist/mygo) | MIT |
| [github.com/Microsoft/go-winio](https://github.com/microsoft/go-winio)（仅 Windows） | MIT |
| [github.com/coder/websocket](https://github.com/coder/websocket) | ISC |
| [github.com/klauspost/compress](https://github.com/klauspost/compress) | BSD-3-Clause（含 Apache-2.0 与 MIT 组件） |
| [github.com/oschwald/maxminddb-golang/v2](https://github.com/oschwald/maxminddb-golang) | ISC |
| [github.com/ebitengine/purego](https://github.com/ebitengine/purego) | Apache-2.0 |
| [github.com/go-text/typesetting](https://github.com/go-text/typesetting) | Unlicense OR BSD-3-Clause |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) | BSD-3-Clause |
| [golang.org/x/image](https://pkg.go.dev/golang.org/x/image) | BSD-3-Clause |
| [golang.org/x/net](https://pkg.go.dev/golang.org/x/net) | BSD-3-Clause |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | BSD-3-Clause |
| [golang.org/x/text](https://pkg.go.dev/golang.org/x/text) | BSD-3-Clause |
| [gopkg.in/yaml.v3](https://github.com/go-yaml/yaml/tree/v3) | MIT 与 Apache-2.0 |

各模块的许可证文件收在包内 `licenses/go/<模块>@<版本>/`；被编入的包所在子目录若另有许可证文件，也一并收在对应子目录下。

## iOS 包隧道扩展（PacketTunnel.appex，仅 iOS）

iOS 不能启动子进程，所以 iOS 版的 Mihomo 不是随包的独立程序，而是以 Go 库的形式编进包隧道扩展 `PacketTunnel.appex`（源码：[`mobile/ios/tunnelcore`](mobile/ios/tunnelcore)、[`mobile/ios/PacketTunnel`](mobile/ios/PacketTunnel)）。该扩展是与 Mihomo v1.19.34 等 GPL-3.0 代码静态链接的组合作品，整体按 GNU General Public License v3.0 分发；澜流自己的这部分代码（MIT）与 GPL-3.0 兼容，其完整源代码即本仓库对应 tag。澜流主程序不链接 Mihomo，许可证不受影响。

On iOS, Mihomo v1.19.34 is linked as a library into the packet tunnel extension `PacketTunnel.appex`, which as a whole is distributed under the GNU General Public License, version 3. Its complete corresponding source code is this repository at the matching tag together with the modules listed below at the versions in [`mobile/ios/tunnelcore/go.mod`](mobile/ios/tunnelcore/go.mod).

| 组件 | 说明 |
| --- | --- |
| 内核 | [Mihomo](https://github.com/MetaCubeX/mihomo) v1.19.34，未作修改，以库方式编入；构建标签 `with_gvisor,with_low_memory,no_tailscale,no_zerotier,no_easytier` |
| 许可证 | GNU General Public License v3.0（<https://github.com/MetaCubeX/mihomo/blob/v1.19.34/LICENSE>） |
| 对应源代码 | <https://github.com/MetaCubeX/mihomo/tree/v1.19.34> 与下表各模块的同版本源码 |

扩展编入的 Go 模块（`GOOS=ios`，按上述构建标签，版本以 `mobile/ios/tunnelcore/go.mod` 为准）：

| 模块 | 许可证 |
| --- | --- |
| github.com/ajg/form | BSD-3-Clause |
| github.com/andybalholm/brotli | MIT |
| github.com/bahlo/generic-list-go | BSD-3-Clause |
| github.com/bodgit/plumbing | BSD-3-Clause |
| github.com/bodgit/windows | BSD-3-Clause |
| github.com/dlclark/regexp2 | MIT |
| github.com/dunglas/httpsfv | BSD-3-Clause |
| github.com/enfein/mieru/v3 | GPL-3.0 |
| github.com/ericlagergren/aegis | BSD-2-Clause |
| github.com/ericlagergren/polyval | BSD-2-Clause |
| github.com/ericlagergren/siv | BSD-3-Clause |
| github.com/ericlagergren/subtle | BSD-3-Clause |
| github.com/fsnotify/fsnotify | BSD-3-Clause |
| github.com/gaukas/godicttls | BSD-3-Clause |
| github.com/gobwas/httphead | MIT |
| github.com/gobwas/pool | MIT |
| github.com/gobwas/ws | MIT |
| github.com/gofrs/uuid/v5 | MIT |
| github.com/golang/snappy | BSD-3-Clause |
| github.com/google/btree | Apache-2.0 |
| github.com/google/pprof | Apache-2.0 |
| github.com/hashicorp/golang-lru/v2 | MPL-2.0 |
| github.com/insomniacslk/dhcp | BSD-3-Clause |
| github.com/josharian/native | MIT |
| github.com/klauspost/compress | Apache-2.0 |
| github.com/klauspost/cpuid/v2 | MIT |
| github.com/klauspost/reedsolomon | MIT |
| github.com/metacubex/age | BSD-3-Clause |
| github.com/metacubex/amneziawg-go | MIT |
| github.com/metacubex/ascon | BSD-3-Clause |
| github.com/metacubex/bart | MIT |
| github.com/metacubex/bbolt | MIT |
| github.com/metacubex/blake3 | MIT |
| github.com/metacubex/chacha | 未附许可证文件 |
| github.com/metacubex/chi | MIT |
| github.com/metacubex/connect-ip-go | MIT |
| github.com/metacubex/cpu | BSD-3-Clause |
| github.com/metacubex/edwards25519 | BSD-3-Clause |
| github.com/metacubex/fswatch | GPL-3.0-or-later |
| github.com/metacubex/gvisor | Apache-2.0 |
| github.com/metacubex/hkdf | BSD-3-Clause |
| github.com/metacubex/hpke | BSD-3-Clause |
| github.com/metacubex/http | BSD-3-Clause |
| github.com/metacubex/jls-quic-go | MIT |
| github.com/metacubex/jls-tls | BSD-3-Clause |
| github.com/metacubex/kcp-go | MIT |
| github.com/metacubex/mhurl | BSD-3-Clause |
| github.com/metacubex/mihomo | GPL-3.0 |
| github.com/metacubex/mipstack | MPL-2.0 |
| github.com/metacubex/mlkem | BSD-3-Clause |
| github.com/metacubex/qpack | MIT |
| github.com/metacubex/quic-go | MIT |
| github.com/metacubex/randv2 | GPL-3.0 |
| github.com/metacubex/restls-client-go | BSD-3-Clause |
| github.com/metacubex/sevenzip | BSD-3-Clause |
| github.com/metacubex/sing | GPL-3.0-or-later |
| github.com/metacubex/sing-mux | GPL-3.0-or-later |
| github.com/metacubex/sing-quic | GPL-3.0-or-later |
| github.com/metacubex/sing-shadowsocks | GPL-3.0-or-later |
| github.com/metacubex/sing-shadowsocks2 | GPL-3.0-or-later |
| github.com/metacubex/sing-tun | GPL-3.0-or-later |
| github.com/metacubex/sing-vmess | GPL-3.0-or-later |
| github.com/metacubex/sing-wireguard | GPL-3.0 |
| github.com/metacubex/smux | MIT |
| github.com/metacubex/ssh | BSD-3-Clause |
| github.com/metacubex/tfo-go | MIT |
| github.com/metacubex/tls | BSD-3-Clause |
| github.com/metacubex/utls | BSD-3-Clause |
| github.com/metacubex/wireguard-go | MIT |
| github.com/metacubex/yamux | MPL-2.0 |
| github.com/miekg/dns | BSD-3-Clause |
| github.com/mroth/weightedrand/v2 | MIT |
| github.com/oasisprotocol/deoxysii | MIT |
| github.com/openacid/low | MIT |
| github.com/oschwald/maxminddb-golang | ISC |
| github.com/pierrec/lz4/v4 | BSD-3-Clause |
| github.com/rasky/go-lzo | GPL-3.0 |
| github.com/RyuaNerin/go-krypto | MIT |
| github.com/samber/lo | MIT |
| github.com/sina-ghaderi/poly1305 | GPL-3.0 |
| github.com/sina-ghaderi/rabaead | GPL-3.0 |
| github.com/sina-ghaderi/rabbitio | Apache-2.0 |
| github.com/sirupsen/logrus | MIT |
| github.com/stangelandcl/ppmd | MIT |
| github.com/u-root/uio | BSD-3-Clause |
| github.com/ulikunitz/xz | BSD-2-Clause |
| github.com/vmihailenco/msgpack/v5 | BSD-2-Clause |
| github.com/vmihailenco/tagparser/v2 | BSD-2-Clause |
| github.com/Yawning/aez | CC0-1.0 |
| github.com/yosida95/uritemplate/v3 | BSD-3-Clause |
| gitlab.com/go-extension/aes-ccm | MIT |
| gitlab.com/yawning/bsaes.git | MIT |
| go.yaml.in/yaml/v3 | Apache-2.0 |
| go4.org/netipx | BSD-3-Clause |
| golang.org/x/crypto | BSD-3-Clause |
| golang.org/x/exp | BSD-3-Clause |
| golang.org/x/net | BSD-3-Clause |
| golang.org/x/sync | BSD-3-Clause |
| golang.org/x/sys | BSD-3-Clause |
| golang.org/x/text | BSD-3-Clause |
| golang.org/x/time | BSD-3-Clause |
| google.golang.org/protobuf（替换为 github.com/metacubex/protobuf-go） | BSD-3-Clause |
