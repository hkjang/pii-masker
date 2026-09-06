package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"pii-masker/internal/app"
	"pii-masker/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	server, err := app.New(cfg)
	if err != nil {
		log.Fatalf("failed to initialize app: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("pii-masker listening on %s", cfg.Server.Address)
	if err := server.Run(ctx); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
	log.Print("pii-masker stopped")
}
