// notifier subscribes to notify.requested and sends
// screenshots + score summaries via Telegram Bot API.
// Can be extended with Slack, email, webhooks etc.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/forge-ai/forge/shared/events"
	"github.com/forge-ai/forge/shared/mq"
	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const telegramAPI = "https://api.telegram.org/bot"

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	_ = godotenv.Load()

	amqpURL := envOr("AMQP_URL", "amqp://forge:forge@rabbitmq:5672/")
	tgToken := envOr("TELEGRAM_BOT_TOKEN", "")
	tgChat  := envOr("TELEGRAM_CHAT_ID", "")

	broker, err := mq.New(amqpURL)
	if err != nil {
		log.Fatal().Err(err).Msg("mq connect")
	}
	defer broker.Close()

	deliveries, err := broker.Subscribe("svc.notifier", events.NotifyRequested)
	if err != nil {
		log.Fatal().Err(err).Msg("subscribe")
	}

	log.Info().Bool("telegram", tgToken != "").Msg("notifier service started")

	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigs; cancel() }()

	n := &notifier{
		tgToken: tgToken,
		tgChat:  tgChat,
		http:    &http.Client{Timeout: 30 * time.Second},
	}

	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-deliveries:
			if !ok {
				return
			}
			if err := handle(ctx, d, n); err != nil {
				log.Error().Err(err).Msg("notify error")
				d.Nack(false, false)
			} else {
				d.Ack(false)
			}
		}
	}
}

func handle(ctx context.Context, d amqp.Delivery, n *notifier) error {
	p, err := events.Unwrap[events.NotifyRequestedPayload](d.Body)
	if err != nil {
		return err
	}

	log.Info().
		Str("job", p.JobID).
		Str("screen", p.ScreenName).
		Str("platform", p.Platform).
		Float64("score", p.Score).
		Msg("sending notification")

	msg := fmt.Sprintf(
		"✅ *%s* [%s] complete!\n"+
			"Similarity: *%.1f%%*\n"+
			"Iterations: %d\n"+
			"`job: %s`",
		p.ScreenName, p.Platform, p.Score, p.Iterations, p.JobID,
	)

	if n.tgToken == "" {
		log.Warn().Msg("TELEGRAM_BOT_TOKEN not set — skipping notification")
		return nil
	}

	// Download diff image if available
	var imgData []byte
	if p.DiffImageURL != "" {
		imgData, _ = n.downloadImage(ctx, p.DiffImageURL)
	}

	if len(imgData) > 0 {
		return n.sendPhoto(ctx, msg, imgData)
	}
	return n.sendMessage(ctx, msg)
}

// ── Notifier ──────────────────────────────────────────────────────────────────

type notifier struct {
	tgToken string
	tgChat  string
	http    *http.Client
}

func (n *notifier) sendMessage(ctx context.Context, text string) error {
	body, _ := json.Marshal(map[string]string{
		"chat_id":    n.tgChat,
		"text":       text,
		"parse_mode": "Markdown",
	})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		telegramAPI+n.tgToken+"/sendMessage",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram %d: %s", resp.StatusCode, b)
	}
	return nil
}

func (n *notifier) sendPhoto(ctx context.Context, caption string, imgData []byte) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", n.tgChat)
	_ = w.WriteField("caption", caption)
	_ = w.WriteField("parse_mode", "Markdown")
	part, _ := w.CreateFormFile("photo", "diff.png")
	part.Write(imgData)
	w.Close()

	req, _ := http.NewRequestWithContext(ctx, "POST",
		telegramAPI+n.tgToken+"/sendPhoto",
		&buf,
	)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendPhoto %d: %s", resp.StatusCode, b)
	}
	return nil
}

func (n *notifier) downloadImage(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := n.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
