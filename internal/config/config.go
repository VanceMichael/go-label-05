package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL, Address       string
	SessionTTL, WorkerInterval time.Duration
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), Address: env("FARM_ADDR", "127.0.0.1:8080"), SessionTTL: duration("FARM_SESSION_TTL", 12*time.Hour), WorkerInterval: duration("FARM_WORKER_INTERVAL", 2*time.Second)}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func duration(k string, d time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	parsed, e := time.ParseDuration(v)
	if e != nil {
		return d
	}
	return parsed
}
func Port(value string) int { n, _ := strconv.Atoi(value); return n }
