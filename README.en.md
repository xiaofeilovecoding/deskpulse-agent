# DeskPulse Agent

DeskPulse Agent is a Windows desktop resource monitor and on-demand diagnostic utility. This preview includes a native tray and floating monitor, user-session process view, local CPU and memory findings, opt-in DeepSeek-compatible analysis, read-only metrics export, and a reversible archive for its own expired diagnostic snapshots.

## Build on Windows x64

```powershell
$env:CGO_ENABLED = '0'
go test ./...
go vet ./...
go build -trimpath -ldflags '-H=windowsgui' -o dist/deskpulse-agent.exe ./cmd/deskpulse-agent
```

Run `dist/deskpulse-agent.exe` as a regular Windows user. Hover over the floating ball to see metrics, or right-click the ball or tray icon for configuration, local diagnostics, exports, and quit. The compiled app does not require a Go, Python, or Node installation. The tray and local diagnostics can switch languages; other preview pages are primarily in Chinese.

Probes and incident delivery are opt-in. Diagnostic records and settings stay under `%LOCALAPPDATA%\DeskPulseAgent`, with provider credentials protected by Windows DPAPI. The optional `--event-webhook` argument enables anonymized incident delivery to one explicit HTTPS destination. A local JSON export is available via the tray menu.

[中文](README.md) · [Current release status](docs/preview.md) · [Third-party notices](THIRD_PARTY_NOTICES.md) · [License](LICENSE)
