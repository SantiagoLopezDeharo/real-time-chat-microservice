package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	DefaultPort            = "8080"
	DefaultMongoURI        = "mongodb://localhost:27017"
	DefaultMongoDB         = "chatdb"
	DefaultMongoCollection = "messages"
	DefaultRetryAttempts   = 5
	DefaultRateLimitRPS    = 5
	DefaultRateLimitBurst  = 10
	DefaultMaxBodyBytes    = 64 << 10
)

type Config struct {
	Port            string
	JWTSecret       string
	MongoURI        string
	MongoDB         string
	MongoCollection string
	RetryAttempts   int
	RateLimitRPS    float64
	RateLimitBurst  int
	MaxBodyBytes    int64
	PprofAddr       string
}

func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		Port:            valueOr(getenv("PORT"), DefaultPort),
		JWTSecret:       getenv("JWT_SECRET"),
		MongoURI:        valueOr(getenv("MONGO_URI"), DefaultMongoURI),
		MongoDB:         valueOr(getenv("MONGO_DB"), DefaultMongoDB),
		MongoCollection: valueOr(getenv("MONGO_COLLECTION"), DefaultMongoCollection),
		PprofAddr:       getenv("PPROF_ADDR"),
	}

	var errs []error
	if cfg.JWTSecret == "" {
		errs = append(errs, errors.New("JWT_SECRET is required"))
	}

	var err error
	if cfg.RetryAttempts, err = parseInt(getenv, "RETRY_ATTEMPTS", DefaultRetryAttempts); err != nil {
		errs = append(errs, err)
	}
	if cfg.RateLimitBurst, err = parseInt(getenv, "RATE_LIMIT_BURST", DefaultRateLimitBurst); err != nil {
		errs = append(errs, err)
	}
	maxBody, err := parseInt(getenv, "MAX_BODY_BYTES", DefaultMaxBodyBytes)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.MaxBodyBytes = int64(maxBody)
	if cfg.RateLimitRPS, err = parseRate(getenv("RATE_LIMIT_RPS")); err != nil {
		errs = append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func parseInt(getenv func(string) string, key string, fallback int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, raw)
	}
	if value < 1 {
		return 0, fmt.Errorf("%s must be >= 1, got %d", key, value)
	}
	return value, nil
}

func parseRate(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultRateLimitRPS, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || value <= 0 || value > 1e12 {
		return 0, fmt.Errorf("RATE_LIMIT_RPS must be a positive number, got %q", raw)
	}
	return value, nil
}
