// Package mq provides a shared RabbitMQ client for all Forge microservices.
// Uses a topic exchange so services subscribe to routing key patterns.
package mq

import (
	"context"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog/log"
)

const (
	Exchange     = "forge.events"
	ExchangeType = "topic"
)

// Broker wraps an AMQP connection with auto-reconnect.
type Broker struct {
	url  string
	conn *amqp.Connection
	ch   *amqp.Channel
}

// New connects to RabbitMQ and declares the exchange.
func New(amqpURL string) (*Broker, error) {
	b := &Broker{url: amqpURL}
	if err := b.connect(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Broker) connect() error {
	var err error
	for attempt := 1; attempt <= 10; attempt++ {
		b.conn, err = amqp.Dial(b.url)
		if err == nil {
			break
		}
		log.Warn().Err(err).Int("attempt", attempt).Msg("RabbitMQ connection failed — retrying")
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	if err != nil {
		return fmt.Errorf("rabbitmq connect after 10 attempts: %w", err)
	}

	b.ch, err = b.conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}

	// Declare durable topic exchange
	return b.ch.ExchangeDeclare(
		Exchange,
		ExchangeType,
		true,  // durable
		false, // auto-deleted
		false, // internal
		false, // no-wait
		nil,
	)
}

// Publish sends a message to the topic exchange with the given routing key.
func (b *Broker) Publish(ctx context.Context, routingKey string, body []byte) error {
	return b.ch.PublishWithContext(ctx,
		Exchange,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Timestamp:    time.Now(),
			Body:         body,
		},
	)
}

// Subscribe binds a named queue to the exchange using a routing key pattern.
// Pattern examples: "job.*", "figma.#", "diff.complete"
func (b *Broker) Subscribe(queueName, pattern string) (<-chan amqp.Delivery, error) {
	q, err := b.ch.QueueDeclare(
		queueName,
		true,  // durable
		false, // auto-delete
		false, // exclusive
		false, // no-wait
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("declare queue %s: %w", queueName, err)
	}

	if err := b.ch.QueueBind(q.Name, pattern, Exchange, false, nil); err != nil {
		return nil, fmt.Errorf("bind queue %s to %s: %w", queueName, pattern, err)
	}

	// Prefetch 1 — process one message at a time per worker
	if err := b.ch.Qos(1, 0, false); err != nil {
		return nil, fmt.Errorf("set qos: %w", err)
	}

	return b.ch.Consume(
		q.Name,
		"",    // consumer tag — auto-generated
		false, // auto-ack — we ack manually after processing
		false, false, false, nil,
	)
}

// Close shuts down channel and connection.
func (b *Broker) Close() {
	if b.ch != nil {
		b.ch.Close()
	}
	if b.conn != nil {
		b.conn.Close()
	}
}
