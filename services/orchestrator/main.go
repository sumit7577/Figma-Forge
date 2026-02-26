// Orchestrator is the brain of Forge.
// It subscribes to ALL events and drives the pipeline state machine:
//
//   job.submitted
//     → figma.parse.requested
//     ← figma.parsed
//     → [for each screen × platform] codegen.requested
//     ← codegen.complete
//     → sandbox.build.requested
//     ← sandbox.ready
//     → diff.requested
//     ← diff.complete
//       if passed  → notify.requested + screen.done
//       if failed  → codegen.requested (next iteration)
//       if max iter → screen.done (best effort)
//     → job.done (when all screens × platforms complete)
//
// It also:
//   - Broadcasts log.event messages for the frontend WebSocket relay
//   - Persists state to Supabase
//   - Exposes REST + WebSocket API for the React frontend
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/forge-ai/forge/services/orchestrator/internal"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if os.Getenv("DEBUG") == "1" {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	_ = godotenv.Load()

	cfg := internal.ConfigFromEnv()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Info().Msg("shutdown signal — stopping orchestrator")
		cancel()
	}()

	o, err := internal.NewOrchestrator(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start orchestrator")
	}
	defer o.Close()

	printBanner()
	log.Info().
		Str("amqp", cfg.AMQPURL).
		Str("api_port", cfg.APIPort).
		Msg("orchestrator online")

	if err := o.Run(ctx); err != nil && err != context.Canceled {
		log.Fatal().Err(err).Msg("orchestrator exited")
	}
}

func printBanner() {
	log.Info().Msg("╔══════════════════════════════════════╗")
	log.Info().Msg("║  FORGE  Orchestrator  v0.2           ║")
	log.Info().Msg("║  Figma → Web + Mobile  AI Agent      ║")
	log.Info().Msg("╚══════════════════════════════════════╝")
}
