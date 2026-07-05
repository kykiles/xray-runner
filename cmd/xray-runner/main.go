package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"

	"xray-runner/internal/app"
	"xray-runner/internal/config"
	"xray-runner/internal/log"
)

func main() {
	cfg, err := config.Load(".env")
	if err != nil {
		log.Fatalf("❌ %v", err)
	}

	defer log.Init(cfg)()

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
```

Wait, I'm importing "fmt" but didn't add it. Let me fix that. Also I used `slog` but it's imported via `log/slog`. Actually I do need `fmt` for the final message. Let me rewrite.<｜end▁of▁thinking｜>

<｜｜DSML｜｜tool_calls>
<｜｜DSML｜｜invoke name="edit">
<｜｜DSML｜｜parameter name="filePath" string="true">D:\user\efimov_p\USB-Flash\projects\Xray\cmd\xray-runner\main.go