# CI 说明（GitHub Actions）

本文档面向项目成员，说明本仓库 GitHub Actions 流水线的目的、阶段规划、
产物获取方式、本机复现命令，以及自托管 runner 的部署概览。

## 1. CI 目的

自动化质量门禁与产物构建，减少人工操作、保证每次入主干（push / PR）的代码
符合本地已验证的标准（与 docs/DESIGN.md 的验收口径一致）：

- **test**（`.github/workflows/test.yml`，Ubuntu）：
  - `go test -race ./...`（fyne 用 headless test driver，无需显示服务器）
  - `go vet ./...`
  - gofmt 格式检查：`test -z "$(gofmt -l .)"`，有未格式化文件时列出并失败
  - Windows 交叉编译门禁：仅对**非 fyne** 的纯 Go 包做 `GOOS=windows GOARCH=amd64
    CGO_ENABLED=0 go build`（fyne.io/systray 为纯 cgo，Linux 下无 mingw 交叉链，
    无法交叉编译 gui/tray/cmd 主包；Windows 原生产物走 build-windows）
- **build-windows**（`.github/workflows/build-windows.yml`，windows-latest）：
  - 原生环境构建 `cmd/ddns-hosts-sync` 主包（含托盘 GUI），无 cgo 墙
  - 产出单文件 exe + zip（内含 exe 与 README.txt 冒烟说明），上传为 artifact 保存 14 天

## 2. 三阶段路线

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| 阶段 0 | 本文件说明 + 工作流推送（其后不会自动触发构建，等阶段 1 接管） | 进行中 |
| 阶段 1 | 云构建出包：test / build-windows 在 GitHub 托管 runner 上跑通，Actions 页面下载 zip 冒烟 | 待推送验证 |
| 阶段 2 | 自托管 runner：如云构建额度受限或需要内网目标机直接构建/冒烟，部署自托管 runner（见 §5） | 计划占位 |

阶段 1 是近期目标：推送工作流后人工在 GitHub Actions 页面上确认两条流水线
变绿，并下载 build-windows 的 zip 在 Windows 目标机冒烟。

## 3. 下载构建产物（artifact）

1. 打开仓库页面，进入 **Actions** 标签页。
2. 左侧工作流列表选择 **build-windows**，点开最近一次成功（绿勾）的运行。
3. 运行详情页底部 **Artifacts** 区域，点 **ddns-hosts-sync-windows-amd64** 下载 zip。
4. 解压后在 Windows 目标机按 zip 内 `README.txt` 的冒烟步骤体验：
   `version` → `install` → `sync` → `tray`（管理员权限 PowerShell）。

> artifact 默认保留 14 天（retention-days: 14），过期后需重新触发构建；
> 需要长期留档的正式版本请走发布流程，不要依赖 artifact。

## 4. 本机复现 CI 检查命令

开发机（Ubuntu，与 test 工作流步骤一一对应）：

```bash
# 1) 测试（含 race）
go test -race ./...

# 2) 静态检查
go vet ./...

# 3) gofmt 格式检查（有输出即失败）
test -z "$(gofmt -l .)"
# 或查看具体不一致文件：gofmt -l .

# 4) Windows 交叉编译门禁（仅纯 Go 包；gui/tray/cmd 依赖 fyne cgo 无法交叉编译）
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  ./internal/config ./internal/hostsfile ./internal/logging \
  ./internal/model ./internal/platform ./internal/resolver \
  ./internal/state ./internal/sync ./internal/tasks
```

Windows 原生产物构建（需要 mingw gcc，对应 build-windows 工作流）：

```powershell
go build -trimpath -ldflags "-s -w" -o dist\ddns-hosts-sync.exe .\cmd\ddns-hosts-sync
Compress-Archive -LiteralPath dist\ddns-hosts-sync.exe, dist\README.txt `
  -DestinationPath dist\ddns-hosts-sync-windows-amd64.zip -Force
```

工作流文件本身可用以下命令静态校验：

```bash
# YAML 语法
python3 -c "import yaml,sys; [yaml.safe_load(open(f)) for f in sys.argv[1:]]" .github/workflows/*.yml
# actionlint（若已安装）
~/go/bin/actionlint .github/workflows/*.yml
```

## 5. 自托管 runner 部署概览（阶段 2 占位）

适用场景：云构建额度告罄、或需在内网/特定 Windows 目标机上出包并直接冒烟。

1. 仓库页面 **Settings → Actions → Runners → New self-hosted runner**。
2. 选择 **Windows x64**，页面会展示一条下载地址与注册 token（token 短期有效，
   只在注册那一刻用于 `config.cmd`，**不要**写入仓库或文档）。
3. 在 Windows 机上解压 runner 包，管理员 PowerShell 依次执行：
   - `./config.cmd --url https://github.com/<owner>/<repo> --token <注册token>`
   - `./run.cmd`（前台运行，验证注册成功并开始接收任务）
4. 建议注册为 Windows 服务开机自启，避免人工每次登录启动：
   按官方**服务化指引**配置（见下方链接），配置完成后可停止前台 `run.cmd`。
5. 需要自托管出包的工作流 job 将 `runs-on` 改为
   `[self-hosted, windows, x64]` 即可切换（commit 时再改，阶段 2 落地时进行）。

官方文档（以 GitHub 页面实时内容为准）：

- 添加自托管 runner：
  https://docs.github.com/zh/actions/hosting-your-own-runners/managing-self-hosted-runners/adding-self-hosted-runners
- runner 应用注册为服务：
  https://docs.github.com/zh/actions/hosting-your-own-runners/configuring-the-self-hosted-runner-application-as-a-service

> 部署细节（token 处理、防火墙/代理、标签命名）以官方文档与仓库实际环境为准，
> 本文档仅给概览，不写死具体值。
