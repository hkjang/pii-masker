package main

import (
	"log"

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

	log.Printf("pii-masker listening on %s", cfg.Server.Address)
	if err := server.Run(); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
