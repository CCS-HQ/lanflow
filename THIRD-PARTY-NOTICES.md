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
