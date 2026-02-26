module github.com/forge-ai/forge/services/differ

go 1.22

require (
	github.com/disintegration/imaging v1.6.2
	github.com/forge-ai/forge/shared v0.0.0
	github.com/joho/godotenv v1.5.1
	github.com/rabbitmq/amqp091-go v1.10.0
	github.com/rs/zerolog v1.32.0
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	golang.org/x/image v0.0.0-20191009234506-e7c1f5e7dbb8 // indirect
	golang.org/x/sys v0.12.0 // indirect
)

replace github.com/forge-ai/forge/shared => ../../shared
