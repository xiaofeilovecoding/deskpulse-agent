# 0.1 Preview

## Scope

The preview is the first usable Windows source release. It uses in-process Windows metrics, native Win32 rendering and SQLite. Native windows cover the floating capsule, monitor, process tree and settings. Separating all views into the proposed workbench renderer is still open.

| Area | Preview status |
| --- | --- |
| Monitoring | Native CPU, physical memory, disk throughput and optional ICMP probes. Process details are restricted to the current Windows user and session |
| Interface | Native capsule, tray, overview and process view. Metric visibility and collection use separate tray controls. GPU and full workbench localization are pending |
| Diagnostics | Local CPU and memory findings. User-initiated DeepSeek-compatible Chat Completions. OpenAI Responses and Anthropic Messages are planned |
| Maintenance | Confirmed archive and recovery for the app’s own expired diagnostic snapshots. The archive stays on the same volume. No C/D drive sweep, permanent file deletion or cross-volume cleanup |
| Integration | Manual sanitized metrics snapshot export. CPU and memory incident journal and experimental HTTPS webhook. No enterprise receiving service |
| Packaging | Local Windows x64 build, without Go, Node, Python or WebView2 at runtime. MSI, release signing and formal Windows 11/RDP certification are pending |

The optional webhook is configured with `--event-webhook https://your-approved-receiver.example/events`. It sends event records from `docs/contracts/incident-v1.schema.json`, keeps the URL fixed per run and refuses redirects. No receiver, enrollment service or authentication mechanism ships in this preview. Use an endpoint you control. The default is a local journal under the app’s data directory.

Start with a Windows x64 system using the build commands in the [README](../README.md).

## Verification evidence

- Local Win10 22H2 x64 build with CGO disabled; `go test ./...` and `go vet ./...`.
- Win10 smoke run collected persisted CPU, memory and disk throughput samples with no RTT target configured. The short smoke run does not establish the 15-minute resource budget.
- The executable renders native synthetic screenshots at 100%, 125%, 150% and 200%. [Capsule](preview-assets/native-capsule.png) · [Overview](preview-assets/native-overview.png)
- The app-owned cache tests cover preview, archive, restore, conflict preservation and changed-file detection. The isolated HTTPS test covers queued restart, send, and recovery with event identifiers.
- External model providers, install signing, Windows 11, RDP and full multimonitor interaction remain unverified for this preview.
