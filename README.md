# DeskPulse Agent

轻量 Windows 桌面监测、按需 AI 诊断和本机处置。**0.1 Preview** 面向 Windows x64，先在 Windows 10 22H2 验证。运行时以普通用户启动。

## 当前可用

- 原生托盘、可拖动的悬浮球和图形胶囊。托盘可以分别设置 CPU、内存、磁盘 I/O、延迟和设备支持时的 GPU 显示与采集。GPU 只在监测窗口或胶囊打开时取样。
- Windows 进程内取样、当前用户会话的进程树、本地历史、基于 CPU 或内存趋势的本机诊断。延迟目标由用户设置。
- 模型设置支持 DeepSeek、Chat Completions、OpenAI Responses 和 Anthropic Messages。每次发送前显示接口、模型和数据范围，凭据按接口隔离，Windows 使用用户范围的 DPAPI。
- 手动导出只读 `metrics-v1.json`、本地匿名 CPU 与内存异常记录，以及通过显式 `--event-webhook` 配置的 HTTPS 事件投递。
- 可以预览、归档和恢复 DeskPulse 自己的过期诊断快照。选择另一数据盘时，副本校验通过后才移除来源快照。 [发布状态](docs/preview.md)

![Windows 原生悬浮胶囊，合成数据](docs/preview-assets/native-capsule.png)

## 构建

在 Windows x64 的 PowerShell 中运行。Go 是构建依赖，运行生成的客户端无需安装 Go、Node 或 Python。

```powershell
$env:CGO_ENABLED = '0'
go test ./...
go vet ./...
go build -trimpath -ldflags '-H=windowsgui' -o dist/deskpulse-agent.exe ./cmd/deskpulse-agent
```

启动 `dist/deskpulse-agent.exe`。悬停查看胶囊，点击球打开监测，右键菜单提供设置、本机诊断、导出和退出。数据位于 `%LOCALAPPDATA%\DeskPulseAgent`。公开项目默认关闭网络探测和事件外发，模型仅在用户发起时请求。 [第三方声明](THIRD_PARTY_NOTICES.md) · [English](README.en.md)

界面默认浅色。设置右上角的月亮和太阳可切换主题，托盘菜单也有同一选项。选择会保留到下次启动。

## 接口与验收

[数据格式和公开样例](docs/contracts/README.md) · [发布状态和验收记录](docs/preview.md)

## macOS 菜单栏预览

macOS 13 及以上可用 Go 和 Xcode Command Line Tools 构建 Intel／Apple Silicon 通用应用。菜单栏显示 CPU、近似可用内存及设备提供的磁盘 I/O。进程详情限当前用户，TCP 建连测点默认关闭。AI 诊断按需启动，使用钥匙串凭据，可手动导出脱敏指标。

```sh
bash macos/build.sh
open "dist/DeskPulse Agent.app"
```
