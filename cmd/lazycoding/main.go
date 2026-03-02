package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/bishenghua/lazycoding/internal/agent/claude"
	"github.com/bishenghua/lazycoding/internal/channel"
	fsadapter "github.com/bishenghua/lazycoding/internal/channel/feishu"
	tgadapter "github.com/bishenghua/lazycoding/internal/channel/telegram"
	"github.com/bishenghua/lazycoding/internal/config"
	"github.com/bishenghua/lazycoding/internal/lazycoding"
	"github.com/bishenghua/lazycoding/internal/session"
	"github.com/bishenghua/lazycoding/internal/transcribe"
)

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	// Configure structured logger.
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Log.Level)}
	if cfg.Log.Format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))

	// Build the speech-to-text transcriber (nil if disabled; Feishu ignores it for now).
	tr, err := transcribe.New(cfg.Transcription)
	if err != nil {
		slog.Error("transcription init failed", "err", err)
		os.Exit(1)
	}
	if tr != nil {
		slog.Info("transcription enabled", "backend", cfg.Transcription.Backend)
	}

	// Select channel adapter based on config.
	var ch channel.Channel
	switch {
	case cfg.Feishu.AppID != "":
		fsCh, err := fsadapter.New(cfg)
		if err != nil {
			slog.Error("feishu adapter init", "err", err)
			os.Exit(1)
		}
		ch = fsCh
		slog.Info("using feishu channel")
	case cfg.Telegram.Token != "":
		tgCh, err := tgadapter.New(cfg, tr)
		if err != nil {
			slog.Error("telegram adapter init", "err", err)
			os.Exit(1)
		}
		ch = tgCh
		slog.Info("using telegram channel")
	default:
		slog.Error("no platform configured: set feishu.app_id or telegram.token in config.yaml")
		os.Exit(1)
	}

	runner := claude.New(&cfg.Claude)

	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("cannot find home directory", "err", err)
		os.Exit(1)
	}
	sessionFile := filepath.Join(home, ".lazycoding", "sessions.json")
	store, err := session.NewFileStore(sessionFile)
	if err != nil {
		slog.Error("session store init failed", "err", err)
		os.Exit(1)
	}
	slog.Info("session store loaded", "path", sessionFile)

	b := lazycoding.New(ch, runner, store, cfg)

	// Graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutting down…")
		cancel()
	}()

	slog.Info("lazycoding started", "config", cfgPath)
	if err := b.Run(ctx); err != nil && err != context.Canceled {
		slog.Error("bot exited with error", "err", err)
		os.Exit(1)
	}
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
