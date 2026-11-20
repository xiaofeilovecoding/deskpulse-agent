# 0.1 Preview

## Scope

The preview is the first usable Windows source release. It uses in-process Windows metrics, native Win32 rendering and SQLite. Native windows cover the floating capsule, monitor, process tree and settings. Separating all views into the proposed workbench renderer is still open.

| Area | Preview status |
| --- | --- |
| Monitoring | Native CPU, physical memory, disk throughput and optional ICMP probes. Process details are restricted to the current Windows user and session |
| Interface | Native capsule, tray, overview and process view. Metric visibility and collection use separate tray controls. The Windows overlay shows the busiest GPU engine on supported drivers, sampled while the view is open |
| Diagnostics | Local CPU and memory findings. Windows model settings offer DeepSeek, Chat Completions, OpenAI Responses and Anthropic Messages. Analysis uses the snapshot and destination approved in the confirmation |
| Maintenance | Confirmed archive and recovery for the app’s own expired diagnostic snapshots. Windows supports a separate recovery volume, with a verified backup before removal and no-replace restoration |
| Integration | Manual sanitized metrics snapshot export. CPU and memory incident journal and experimental HTTPS webhook. No enterprise receiving service |
| Packaging | Windows x64 standalone EXE and an optional per-user MSI build. Go, Node, Python and WebView2 are not required at runtime. The preview build is unsigned |
| macOS | Native menu-bar preview targeting macOS 13+, with scoped process details, optional TCP connection timing, sanitized JSON export and on-demand analysis of CPU and memory using Keychain credentials. Hosted UI checks run on macOS 15. Notarization is pending |

The optional webhook is configured with `--event-webhook https://your-approved-receiver.example/events`. It sends event records from `docs/contracts/incident-v1.schema.json`, keeps the URL fixed per run and refuses redirects. No receiver, enrollment service or authentication mechanism ships in this preview. Use an endpoint you control. The default is a local journal under the app’s data directory.

Start with a Windows x64 system using the build commands in the [README](../README.md).

## Verification evidence

- Local Win10 22H2 x64 build with CGO disabled, `go test ./...` and `go vet ./...`.
- Win10 collected persisted CPU, memory and disk throughput samples. A 15-minute idle run measured a P95 private working set of 7.23 MiB and average CPU use of 0.028% of one logical core.
- The executable renders native synthetic screenshots at 100%, 125%, 150% and 200% in English and Simplified Chinese, in light and dark themes. The 48 DIP ball and 54 DIP capsule use Windows per-pixel alpha, with partial coverage on the curved edges at all four scales. [Capsule](preview-assets/native-capsule.png) · [Overview](preview-assets/native-overview.png)
- The cache tests cover cross-volume verification, restoration, changed-file detection and corruption handling. The isolated HTTPS test covers queued restart, send and recovery with event identifiers.
- The per-user MSI passed Windows 10 installation, in-place upgrade to a test version and uninstall. The bundled executable matched the standalone candidate and rendered a Settings frame. Uninstall removed the program and shortcut and preserved synthetic user data.
- A Windows 11 24H2 x64 evaluation VM installed and removed the candidate MSI and rendered the EXE at four scales in its console and in an RDP session after reconnecting. A 15-minute RDP idle run measured a P95 private working set of 6.38 MiB and average CPU use of 0.363% of one logical core. CPU, memory and disk I/O samples reached the local database. A separate Windows 11 Arm check ran the x64 executable under Windows emulation. [Windows validation](https://github.com/xiaofeilovecoding/deskpulse-agent/actions/runs/44775611173)
- Hosted macOS 15 checks on Intel and Apple Silicon exercised the native menu bar, English and Simplified Chinese rendering in both themes, the process window and isolated Keychain add, replace, read and delete operations. The system probes cover CPU, memory, processes, local TCP timing and sanitized export. [macOS validation](https://github.com/xiaofeilovecoding/deskpulse-agent/actions/runs/44775611289)
- Model tests use local synthetic services. A live DeepSeek test with synthetic measurements passed Chat Completions, Responses and Messages.
- The preview evidence covers an isolated RDP connection. Enterprise VDI policies, physical multi-monitor interactions and newer hardware combinations depend on the deployment environment. Trusted signing and notarization are pending.
