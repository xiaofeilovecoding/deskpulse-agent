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
| Packaging | Windows x64 standalone EXE and an optional per-user MSI build. Go, Node, Python and WebView2 are not required at runtime. Release signing and Windows 11/RDP qualification are pending |
| macOS | Native menu-bar preview for macOS 13+, scoped process details, optional TCP connection timing, sanitized JSON export and on-demand analysis of CPU and memory using Keychain credentials. Native UI and notarization require macOS release validation |

The optional webhook is configured with `--event-webhook https://your-approved-receiver.example/events`. It sends event records from `docs/contracts/incident-v1.schema.json`, keeps the URL fixed per run and refuses redirects. No receiver, enrollment service or authentication mechanism ships in this preview. Use an endpoint you control. The default is a local journal under the app’s data directory.

Start with a Windows x64 system using the build commands in the [README](../README.md).

## Verification evidence

- Local Win10 22H2 x64 build with CGO disabled, `go test ./...` and `go vet ./...`.
- Win10 smoke run collected persisted CPU, memory and disk throughput samples with no latency target configured. A 15-minute idle run on the current candidate measured a P95 private working set of 7.17 MiB.
- The executable renders native synthetic screenshots at 100%, 125%, 150% and 200% in English and Simplified Chinese, in light and dark themes. The 48 DIP ball and 54 DIP capsule use Windows per-pixel alpha, with partial coverage on the curved edges at all four scales. [Capsule](preview-assets/native-capsule.png) · [Overview](preview-assets/native-overview.png)
- The cache tests cover cross-volume verification, restoration, changed-file detection and corruption handling. The isolated HTTPS test covers queued restart, send and recovery with event identifiers.
- The per-user MSI passed Windows 10 installation, in-place upgrade to a test version and uninstall. The bundled executable matched the standalone candidate and rendered a Settings frame. Uninstall removed the program and shortcut and preserved synthetic user data.
- Model tests use local synthetic services. A live DeepSeek test with synthetic measurements also passed Chat Completions, Responses and Messages. Official OpenAI and Anthropic endpoints, trusted install signing, Windows 11, RDP and full multimonitor interaction remain release checks.
