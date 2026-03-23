package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/discord"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := discord.Run(ctx, cfg); err != nil {
		log.Fatalf("discord bot exited: %v", err)
	}
}
