package config

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func withSecret(extra map[string]string) map[string]string {
	values := map[string]string{"JWT_SECRET": "secret"}
	for k, v := range extra {
		values[k] = v
	}
	return values
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(withSecret(nil)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Port:            DefaultPort,
		JWTSecret:       "secret",
		MongoURI:        DefaultMongoURI,
		MongoDB:         DefaultMongoDB,
		MongoCollection: DefaultMongoCollection,
		RetryAttempts:   DefaultRetryAttempts,
		RateLimitRPS:    DefaultRateLimitRPS,
		RateLimitBurst:  DefaultRateLimitBurst,
		MaxBodyBytes:    DefaultMaxBodyBytes,
	}
	if *cfg != want {
		t.Errorf("Load = %+v, want %+v", *cfg, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(withSecret(map[string]string{
		"PORT":             "9090",
		"MONGO_URI":        "mongodb://db:27017",
		"MONGO_DB":         "db",
		"MONGO_COLLECTION": "coll",
		"RETRY_ATTEMPTS":   "3",
		"RATE_LIMIT_RPS":   "0.5",
		"RATE_LIMIT_BURST": " 20 ",
		"MAX_BODY_BYTES":   "1024",
		"PPROF_ADDR":       "127.0.0.1:6060",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Port:            "9090",
		JWTSecret:       "secret",
		MongoURI:        "mongodb://db:27017",
		MongoDB:         "db",
		MongoCollection: "coll",
		RetryAttempts:   3,
		RateLimitRPS:    0.5,
		RateLimitBurst:  20,
		MaxBodyBytes:    1024,
		PprofAddr:       "127.0.0.1:6060",
	}
	if *cfg != want {
		t.Errorf("Load = %+v, want %+v", *cfg, want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		wantErr []string
	}{
		{"missing secret", map[string]string{}, []string{"JWT_SECRET is required"}},
		{"zero retries", withSecret(map[string]string{"RETRY_ATTEMPTS": "0"}), []string{"RETRY_ATTEMPTS must be >= 1"}},
		{"negative retries", withSecret(map[string]string{"RETRY_ATTEMPTS": "-3"}), []string{"RETRY_ATTEMPTS must be >= 1"}},
		{"non numeric retries", withSecret(map[string]string{"RETRY_ATTEMPTS": "five"}), []string{"RETRY_ATTEMPTS must be an integer"}},
		{"zero burst", withSecret(map[string]string{"RATE_LIMIT_BURST": "0"}), []string{"RATE_LIMIT_BURST must be >= 1"}},
		{"float burst", withSecret(map[string]string{"RATE_LIMIT_BURST": "1.5"}), []string{"RATE_LIMIT_BURST must be an integer"}},
		{"zero body", withSecret(map[string]string{"MAX_BODY_BYTES": "0"}), []string{"MAX_BODY_BYTES must be >= 1"}},
		{"overflow body", withSecret(map[string]string{"MAX_BODY_BYTES": "99999999999999999999"}), []string{"MAX_BODY_BYTES must be an integer"}},
		{"zero rps", withSecret(map[string]string{"RATE_LIMIT_RPS": "0"}), []string{"RATE_LIMIT_RPS must be a positive number"}},
		{"negative rps", withSecret(map[string]string{"RATE_LIMIT_RPS": "-1"}), []string{"RATE_LIMIT_RPS"}},
		{"nan rps", withSecret(map[string]string{"RATE_LIMIT_RPS": "NaN"}), []string{"RATE_LIMIT_RPS"}},
		{"inf rps", withSecret(map[string]string{"RATE_LIMIT_RPS": "Inf"}), []string{"RATE_LIMIT_RPS"}},
		{"text rps", withSecret(map[string]string{"RATE_LIMIT_RPS": "fast"}), []string{"RATE_LIMIT_RPS"}},
		{
			"reports every problem",
			map[string]string{"RETRY_ATTEMPTS": "x", "RATE_LIMIT_BURST": "0", "MAX_BODY_BYTES": "-1", "RATE_LIMIT_RPS": "0"},
			[]string{"JWT_SECRET", "RETRY_ATTEMPTS", "RATE_LIMIT_BURST", "MAX_BODY_BYTES", "RATE_LIMIT_RPS"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(env(tt.values))
			if err == nil {
				t.Fatalf("Load returned nil error and %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("config returned alongside error: %+v", cfg)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}
