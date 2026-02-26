module github.com/forge-ai/forge/services/figma-parser

go 1.22

require (
	github.com/forge-ai/forge/shared v0.0.0
	github.com/rs/zerolog            v1.32.0
	github.com/google/uuid           v1.6.0
	github.com/rabbitmq/amqp091-go   v1.10.0
	github.com/joho/godotenv         v1.5.1
)

replace github.com/forge-ai/forge/shared => ../../shared
