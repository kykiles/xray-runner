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
	"xray-runner/internal/tui"
)

var Version = "dev"

func main() {
	flagVersion := flag.Bool("version", false, "show version")
	flagConfig := flag.String("config", ".env", "path to .env file")
	// U-2: scripted/non-interactive selection.
	flagServer := flag.String("server", "", "server to use: 1-based index or name (skips the menu)")
	flagLast := flag.Bool("last", false, "reuse the last selected subscription/server")
	flagNonInteractive := flag.Bool("non-interactive", false, "never prompt; fail if a choice is required")
	flagDumpLinks := flag.Bool("dump-links", false, "fetch subscription and dump bare links to keys/<DOMAIN>.md, then exit")
	flag.Parse()

	if *flagVersion {
		fmt.Printf("xray-runner %s\n", Version)
		os.Exit(0)
	}

	cfg, err := config.Load(*flagConfig)
	if err != nil {
		log.Fatalf("❌ %v", err)
	}
	// Colors are tunable from .env (task #6); apply them once the environment is
	// loaded, before any TUI screen renders.
	tui.InitStyles()

	if *flagDumpLinks {
		subURL := cfg.SubscriptionURL
		if args := flag.Args(); len(args) > 0 {
			subURL = args[0]
		}
		if subURL == "" {
			fmt.Fprintln(os.Stderr, "❌ dump-links: не задан URL подписки (аргумент или SUBSCRIPTION_URL)")
			os.Exit(2)
		}
		path, err := dumpLinks(cfg, subURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Ссылки сохранены: %s\n", path)
		os.Exit(0)
	}

	defer applog.Init(cfg)()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	slog.Info("starting xray-runner", "version", Version)
	fmt.Print(banner())

	application := app.New(cfg, app.Options{
		Server:         *flagServer,
		UseLast:        *flagLast,
		NonInteractive: *flagNonInteractive,
	})
	if err := application.Run(ctx); err != nil {
		// R-4: user-initiated exits (quit key, Ctrl+C) are not failures; App.Run's
		// deferred cleanup has already released proxies/firewall by this point.
		if errors.Is(err, app.ErrUserQuit) || errors.Is(err, context.Canceled) {
			slog.Info("session ended by user")
			fmt.Println("\n👋 До встречи!")
			return
		}
		slog.Error("fatal", "error", err)
		// Logs go to the file only, so a fatal error must still reach the user.
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		// U-2: distinct exit codes for scripts/systemd — 2 means the requested
		// server/subscription could not be selected, 1 is a runtime failure.
		if errors.Is(err, app.ErrSelection) {
			os.Exit(2)
		}
		os.Exit(1)
	}

	slog.Info("session ended")
	fmt.Println("\n👋 До встречи!")
}
