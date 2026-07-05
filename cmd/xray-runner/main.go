package main

import (
	"context"
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

	slog.Info("starting xray-runner")

	application := app.New(cfg)
	if err := application.Run(ctx); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}

	fmt.Println("\n👋 До встречи!")
}
