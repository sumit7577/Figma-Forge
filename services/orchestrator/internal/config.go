package internal

import (
	"os"
	"strconv"
)

type Config struct {
	AMQPURL          string
	SupabaseURL      string
	SupabaseKey      string
	APIPort          string
	MaxIter          int
	DefaultThreshold int
}

func ConfigFromEnv() Config {
	return Config{
		AMQPURL:          env("AMQP_URL", "amqp://forge:forge@rabbitmq:5672/"),
		SupabaseURL:      env("SUPABASE_URL", ""),
		SupabaseKey:      env("SUPABASE_SERVICE_KEY", ""),
		APIPort:          env("API_PORT", "8080"),
		MaxIter:          envInt("MAX_ITERATIONS", 10),
		DefaultThreshold: envInt("SIMILARITY_TARGET", 95),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		n, _ := strconv.Atoi(v)
		if n > 0 {
			return n
		}
	}
	return def
}
