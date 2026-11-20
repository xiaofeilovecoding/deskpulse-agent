# DeskPulse Agent

DeskPulse Agent is a desktop resource monitor and on-demand diagnostic utility. The Windows preview includes a native tray, floating monitor, user-session process view, local findings, opt-in model analysis, read-only metrics export and verified recovery copies of expired app snapshots. It was verified on Windows 10 22H2 and in a Windows 11 24H2 x64 evaluation VM. [Verification scope](docs/preview.md)

## Build on Windows x64

```powershell
$env:CGO_ENABLED = '0'
go test ./...
go vet ./...
go build -trimpath -ldflags '-H=windowsgui' -o dist/deskpulse-agent.exe ./cmd/deskpulse-agent
```

Run `dist/deskpulse-agent.exe` as a regular Windows user. Hover over the floating ball to view metrics. The ball and tray menus offer configuration, local diagnostics, export and quit. The executable requires no Go, Python or Node installation. The tray language control switches between Simplified Chinese and English.

The desktop starts in light mode. Use the moon or sun button in Settings or the tray menu to change themes. DeskPulse remembers the selection.

Probes and incident delivery are opt-in. The model settings support OpenAI-style Chat Completions and Responses, and Anthropic-style Messages. DeepSeek Chat is a preset. Before any diagnosis, the confirmation identifies the API endpoint, model and evidence scope. Windows DPAPI separates credentials by endpoint and request format. The optional `--event-webhook` argument enables anonymized incident delivery to one HTTPS destination.

## macOS menu-bar preview

On macOS 13 or later with Go and Xcode Command Line Tools, run `bash macos/build.sh` and open `dist/DeskPulse Agent.app`. The universal build targets Intel and Apple Silicon. The menu-bar preview offers CPU, estimated available memory, disk I/O where available, same-user processes, optional TCP connection timing and on-demand model analysis of CPU and memory using Keychain credentials.

Hosted macOS 15 tests on Intel and Apple Silicon cover the native menu bar, both themes, the process window and isolated Keychain credentials.

[中文](README.md) · [Current release status](docs/preview.md) · [Third-party notices](THIRD_PARTY_NOTICES.md) · [License](LICENSE)
