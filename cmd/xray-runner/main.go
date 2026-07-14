package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"

	"xray-runner/internal/app"
	"xray-runner/internal/config"
	applog "xray-runner/internal/log"
)

var Version = "dev"

func main() {
	flagVersion := flag.Bool("version", false, "show version")
	flagConfig := flag.String("config", ".env", "path to .env file")
	flag.Parse()

	if *flagVersion {
		fmt.Printf("xray-runner %s\n", Version)
		os.Exit(0)
	}

	cfg, err := config.Load(*flagConfig)
	if err != nil {
		log.Fatalf("❌ %v", err)
	}

	defer applog.Init(cfg)()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	slog.Info("starting xray-runner", "version", Version)

	application := app.New(cfg)
	if err := application.Run(ctx); err != nil {
		// R-4: user-initiated exits (quit key, Ctrl+C) are not failures; App.Run's
		// deferred cleanup has already released proxies/firewall by this point.
		if errors.Is(err, app.ErrUserQuit) || errors.Is(err, context.Canceled) {
			slog.Info("session ended by user")
			fmt.Println("\n👋 До встречи!")
			return
		}
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}

	slog.Info("session ended")
	fmt.Println("\n👋 До встречи!")
}
