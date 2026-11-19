//go:build windows

// Command deskpulse-agent is the v0.3 console-free Windows GUI application.
// It samples Windows metrics in-process and never starts Telegraf, a local HTTP
// server or an external browser in normal runtime.
package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/xiaofeilovecoding/deskpulse-agent/internal/desktop"
)

func main() {
	fs := flag.NewFlagSet("deskpulse-agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data-dir", "", "数据目录（默认 %LOCALAPPDATA%\\DeskPulse Agent）")
	baseURL := fs.String("deepseek-base-url", "", "仅测试用 DeepSeek 兼容地址（HTTPS 或本机回环 HTTP）")
	webhook := fs.String("event-webhook", "", "显式设置 HTTPS 异常接收端（默认仅本地）")
	showVersion := fs.Bool("version", false, "显示版本")
	synthetic := fs.Bool("synthetic", false, "开发/验收：使用合成数据源，不启动真实采样")
	captureDir := fs.String("capture-dir", "", "开发/验收：把各页面真实绘图表面导出为 PNG 后退出")
	captureDPI := fs.String("capture-dpi", "", "开发/验收：强制布局 DPI（96–480），不指定时输出 100/125/150/200%（96/120/144/192）四套")
	if err := fs.Parse(os.Args[1:]); err != nil {
		desktop.ShowError("参数错误：" + err.Error())
		return
	}
	if *showVersion {
		desktop.ShowInfo("DeskPulse Agent v" + desktop.Version)
		return
	}
	var dpi uint32
	if *captureDPI != "" {
		v, err := strconv.Atoi(*captureDPI)
		if err != nil || v < 96 || v > 480 {
			desktop.ShowError("capture-dpi 需要在 96–480 之间")
			return
		}
		dpi = uint32(v)
	}
	if err := desktop.Run(desktop.Options{
		DataDir:    *dataDir,
		BaseURL:    *baseURL,
		Webhook:    *webhook,
		Version:    desktop.Version,
		Synthetic:  *synthetic,
		CaptureDir: *captureDir,
		CaptureDPI: dpi,
	}); err != nil {
		if *captureDir != "" {
			if os.MkdirAll(*captureDir, 0700) == nil {
				_ = os.WriteFile(filepath.Join(*captureDir, "capture-error.txt"), []byte(err.Error()), 0600)
			}
			os.Exit(1)
		}
		desktop.ShowError("DeskPulse Agent 启动失败：\n" + err.Error())
	}
}
