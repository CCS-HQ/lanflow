# 开发、CI 与发布

面向维护者：本地开发、测试、CI、打包、发布和应用自动更新。用户文档见 [README](../README.md)。

## 本地开发

需要 `go.mod` 中 `go` 指令声明的 Go 版本或更新。先下载官方 Mihomo 与地理数据库到 `bin/`：

```sh
scripts/fetch-core.sh                 # 宿主平台；也可 scripts/fetch-core.sh windows amd64
```

内核版本固定为 `scripts/fetch-core.sh` 中的 `PINNED_VERSION`。脚本按 SHA-256 校验每个文件（内核用脚本内置的官方哈希，地理数据用 GitHub API 给出的 asset digest），不符即退出。内核写到 `bin/<goos>-<goarch>/`，宿主平台另复制一份到 `bin/mihomo`；地理数据写到 `bin/geo/`。网络受限时可设 `GITHUB_MIRROR`，镜像下载的文件同样要通过校验。`MIHOMO_VERSION` 可临时换成其他版本（校验值改向 GitHub API 查询）。

运行（真实服务 / 模拟数据）：

```sh
go tool mygo dev
LANFLOW_FAKE=1 go tool mygo dev
```

澜流分为后台服务进程（`Lanflow --service`）和窗口进程（默认）。窗口进程发现服务没在运行时，会用同一个可执行文件带 `--service` 拉起它；`LANFLOW_SINGLE=1` 以拆分前的单进程方式运行，用于排障。详见 [service-split.md](service-split.md)。

本机接口可以用模拟后端试：`LANFLOW_FAKE=1 LANFLOW_SOCKET=/tmp/lf.sock go run .`，另开终端 `LANFLOW_SOCKET=/tmp/lf.sock go run . ctl status`。

### 测试与检查

```sh
go vet ./... && go tool mygo vet .
CGO_ENABLED=0 go test -shuffle=on ./...
go test -race -shuffle=on ./...       # 需要 cgo（macOS 自带的工具链即可）
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
go mod tidy -diff
```

CI 使用的 staticcheck 与 govulncheck 版本固定在 `.github/workflows/ci.yml` 中；本地结果与 CI 不一致时，以那里的版本为准。

`internal/app`、`internal/core`、`internal/capture/agent` 中的端到端测试使用 `bin/mihomo`，`internal/locate`、`internal/profile` 的部分测试使用 `bin/geo/`；没有这些文件时自动跳过，其余测试不需要网络。端到端测试不会改动系统代理；改动真实系统设置的测试需显式开启：

- `LANFLOW_LIVE_SYSPROXY=1 go test -run LiveSystemProxy ./internal/app/`：实测系统代理（只用直连节点，几秒后自动恢复原设置）。
- `scripts/vm-e2e.sh` 在虚拟机里运行 `LANFLOW_LIVE_E2E` 测试；`scripts/vm-tun-test.sh <虚拟机名>` 实测 TUN（安装权限组件、开启 TUN、关闭后路由恢复、卸载无残留）。

`go run ./tools/landgrid` 重新生成出口地图用的陆地点阵 `internal/geo/land_data.go`（Natural Earth 数据）。

## CI

`.github/workflows/ci.yml` 在 PR 和推送到 main 时运行，也可以手动触发。每个作业都设了超时。

- **Lint and cross builds**（Linux）：
  - 权限组件版本检查：组件代码或它依赖的模块有改动而 `cmd/lanflow-helper/VERSION` 没变时报错（`scripts/check-helper-version.sh`）。
  - `go mod tidy -diff`。
  - 第三方声明：`THIRD-PARTY-NOTICES.md` 写的 Mihomo 版本与 `fetch-core.sh` 的 `PINNED_VERSION` 一致，并列全了编入应用和权限组件的 Go 模块。
  - `mygo vet`；对 darwin/arm64、windows/amd64、linux/amd64、windows/arm64 执行 `go vet`，对 linux/amd64、windows/arm64 执行 `go build`。
  - staticcheck、govulncheck：都是必过项，有告警即失败。
- **Test**（macOS、Windows）：先用 `fetch-core.sh` 下载内核和地理数据（有缓存；仓库变量 `CI_SKIP_CORE=1` 可跳过下载），再跑一遍 `go test`，都带 `-shuffle=on`，macOS 另带 `-race`。失败时完整的测试输出（含界面测试打印的页面元素位置）作为 `test-output-<系统>` 产物保留 14 天；输出里的 `-shuffle=<种子>` 可用来在本地复现同样的顺序。

## 打包

```sh
scripts/prepare-resources.sh darwin arm64     # 内核、权限组件、地理数据库、许可证材料 → resources/
scripts/build-app.sh darwin arm64             # build/darwin-arm64/Lanflow.app 与 .dmg
```

平台参数可换成 `darwin amd64`；Windows 安装程序要在 Windows 上（Git Bash）用 `windows amd64` 执行这两步。

要用 `scripts/build-app.sh` 而不是直接 `go tool mygo build`：`prepare-resources.sh` 会把权限组件的版本和（macOS 上）它的 SHA-256 写到 `bin/<平台>/app-ldflags`，`build-app.sh` 通过 `MYGO_LDFLAGS` 把它们链接进应用，安装 TUN 权限组件前据此核对。直接构建出的应用没有这层校验，与发布版不一致。

`prepare-resources.sh` 同时生成 `resources/<平台>/licenses/`（由 `scripts/collect-licenses.sh` 完成），随应用一起打包：Mihomo 的 GPL-3.0 许可证全文（`fetch-core.sh` 从 Mihomo 仓库的对应 tag 下载并校验）、指向该版本源代码的说明、地理数据库的来源与署名，以及编入程序的每个 Go 模块的许可证文件（从本机 Go module cache 复制）。清单见 [THIRD-PARTY-NOTICES.md](../THIRD-PARTY-NOTICES.md)。

没有设置 `MYGO_UPDATER_PRIVATE_KEY` 时，构建只产出应用和安装包，不产出更新包与更新清单（见下文「应用自动更新」）。

## 发布

1. 改版本号，保持一致（发布流程会核对前两处，不一致即失败）：
   - `mygo.json` 的 `version`；
   - `internal/view/settings/page.go` 的 `appVersion` 常量（未打包运行时「关于」里显示的版本）；
   - 权限组件的代码或它的依赖有改动时，另把 `cmd/lanflow-helper/VERSION` 加一（CI 和发布流程都会检查；已安装的权限组件只在版本号不同时才会被替换）。
2. 升级内核固定版本时，同步修改 `scripts/fetch-core.sh` 的 `PINNED_VERSION` 与各平台 SHA-256、`internal/app/coredl.go` 的 `pinnedCoreVersion`、`internal/app/coresums.go` 的 SHA-256，以及 `THIRD-PARTY-NOTICES.md` 里的版本号与链接（测试和 CI 会核对）。各平台的 SHA-256 取自 Mihomo 发布页的 asset digest。
3. 提交并推送到 main，等这个提交的 CI 通过。
4. 在这个提交上打同名 tag（`v` 加 `mygo.json` 的版本号）并推送：

   ```sh
   v="v$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' mygo.json)"
   git tag "$v" && git push origin "$v"
   ```

GitHub Actions（`.github/workflows/release.yml`）随后：

- **校验**（validate）：tag 与上述版本号一致；tag 指向的提交在 main 上；更新签名密钥 `MYGO_UPDATER_PRIVATE_KEY` 已配置；权限组件版本检查通过。地理数据库上游只有每天滚动更新的 `latest`，所以在这里只下载一次，两个平台的构建作业使用同一份并按 SHA-256 核对；三个文件的 SHA-256 写进 Release 说明、附件 `lanflow-<tag>-geodata.sha256` 和各平台的构建信息。
- **等待 CI**（wait-ci，与构建并行）：该提交要有一次成功的 CI 运行（推送到 main 或在 tag 上手动触发的才算，PR 的不算）；还在运行就等待，最多 45 分钟；没有或失败则终止，可以在 tag 上手动触发 CI 后重跑。vet、竞态检测、staticcheck、govulncheck、交叉编译和完整的单元 / 界面测试都依赖这次 CI 的结果，构建作业里不再重复。
- **构建**（macOS、Windows）：准备资源后，只对用到真实内核和地理数据的包（`internal/core`、`controller`、`locate`、`profile`、`capture/agent`、`app`）运行测试，确保它们针对将要发布的内核与数据通过；再用 `scripts/build-app.sh` 构建 macOS arm64 / amd64 的 `.dmg` 和 Windows 安装程序，以及签名的更新包、增量包和更新清单；最后核对包内的权限组件摘要、地理数据库和许可证材料。
- **发布**（需要以上全部通过）：附上 SBOM（`lanflow-<tag>.spdx.json`）、各二进制的 `go version -m` 构建信息、`THIRD-PARTY-NOTICES.md`，生成 `SHA256SUMS.txt` 与构建来源证明（attestation），创建 Release（更新说明由 GitHub 自动生成），最后上传更新清单——清单一上传，已安装的应用就能看到新版本。

带 `-` 的 tag（如 `v1.0.0-beta.1`）发布为预发布版。发布流程不使用 Go 构建缓存，所用 action 固定到 commit SHA，由 Dependabot 更新。安装包目前没有 Apple Developer ID 签名、公证和 Windows 代码签名（macOS 为 ad hoc 签名）。

## 应用自动更新

澜流用 MyGo 的签名更新（`mygo.Updater`）：「设置 › 常规 › 关于」里的「检查更新」和「下载并重启」，以及每天启动时的一次自动检查（可在同一处关闭）。「下载并重启」会先让后台服务按正常退出流程结束（恢复系统代理与 DNS、停止解密子进程、停止内核并确认端口释放），再启动新版本。

更新源在 `mygo.json` 的 `updates` 块里：

```json
"updates": {
  "publicKey": "<ed25519 公钥>",
  "github": "Owlbay/lanflow"
}
```

- 已安装的应用读取该仓库最新正式 Release（不含草稿和预发布版）里的 `update-<平台>.json`，按其中的地址下载更新包或增量包，并用 `publicKey` 验证签名后才安装。
- 对应的私钥是仓库 secret `MYGO_UPDATER_PRIVATE_KEY`，发布流程用它为更新包和清单签名。私钥**不要**放进仓库，另在密码管理器里留一份：丢了就无法再向已安装的应用推送更新，泄露则任何人都能向用户下发代码。需要重新生成时用 `go tool mygo keygen`，但换了公钥之后，旧版本不会接受新密钥签名的更新。
- 本地构建若要产出更新包，在构建时提供私钥：`MYGO_UPDATER_PRIVATE_KEY="$(cat /path/to/mygo-update.key)" scripts/build-app.sh darwin arm64`。
- 开发构建（`mygo dev`、`go run`）和由包管理器安装的副本不会自更新；去掉 `updates` 块的构建在「关于」里显示「此版本未配置更新源」。
- 更新窗口里的更新说明来自仓库根目录的 `CHANGELOG.md` 中对应版本的小节（如 `## 1.0.0`）。仓库目前没有这个文件，所以更新清单里不带更新说明，说明只在 GitHub 的 Release 页面上；若以后添加该文件，每个发布的版本都必须有对应小节，否则构建失败。

改用自己的服务器、S3/R2 存放更新，以及清单格式、增量更新等细节，见 [MyGo 文档](https://github.com/egoist/mygo/blob/main/docs/updates.md)。
