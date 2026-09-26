// Package config loads runtime configuration from environment variables with
// sensible development defaults. Every value is prefixed with OFO_.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is the fully-resolved application configuration.
type Config struct {
	Env           string
	HTTP          HTTPConfig
	DB            DBConfig
	Redis         RedisConfig
	Worker        WorkerConfig
	Observability ObservabilityConfig
	Sim           SimConfig
}

// HTTPConfig configures the API server.
type HTTPConfig struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
}

// DBConfig configures the PostgreSQL connection pool.
type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
	MaxConns int
	MinConns int
}

// DSN renders a pgx/libpq keyword-value connection string with pool settings,
// for use with pgxpool.
func (d DBConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s pool_max_conns=%d pool_min_conns=%d",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode, d.MaxConns, d.MinConns,
	)
}

// URL renders a URL-style DSN without pool parameters, for use with the
// database/sql pgx stdlib driver (e.g. the migration runner).
func (d DBConfig) URL() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.Name, d.SSLMode,
	)
}

// RedisConfig configures the Redis client.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// WorkerConfig configures the background job worker.
type WorkerConfig struct {
	Concurrency  int
	PollInterval time.Duration
	ClaimBatch   int
	LockTTL      time.Duration
	ReapInterval time.Duration
}

// ObservabilityConfig configures tracing, metrics, and logging.
type ObservabilityConfig struct {
	OTelEnabled  bool
	OTLPEndpoint string
	ServiceName  string
	MetricsAddr  string
	LogLevel     string
}

// SimConfig tunes simulated failure behaviour of the mock domain services so
// demos and tests can exercise the compensation paths deterministically.
type SimConfig struct {
	PaymentDeclineRate     float64
	FulfillmentFailureRate float64
}

// Load reads configuration from the environment, applying defaults.
func Load() (Config, error) {
	c := Config{
		Env: getStr("OFO_ENV", "development"),
		HTTP: HTTPConfig{
			Addr:            getStr("OFO_HTTP_ADDR", ":8080"),
			ReadTimeout:     getDur("OFO_HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    getDur("OFO_HTTP_WRITE_TIMEOUT", 15*time.Second),
			ShutdownTimeout: getDur("OFO_HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
		},
		DB: DBConfig{
			Host:     getStr("OFO_DB_HOST", "localhost"),
			Port:     getInt("OFO_DB_PORT", 5432),
			User:     getStr("OFO_DB_USER", "ofo"),
			Password: getStr("OFO_DB_PASSWORD", "ofo_dev_password_change_me"),
			Name:     getStr("OFO_DB_NAME", "ofo"),
			SSLMode:  getStr("OFO_DB_SSLMODE", "disable"),
			MaxConns: getInt("OFO_DB_MAX_CONNS", 20),
			MinConns: getInt("OFO_DB_MIN_CONNS", 2),
		},
		Redis: RedisConfig{
			Addr:     getStr("OFO_REDIS_ADDR", "localhost:6379"),
			Password: getStr("OFO_REDIS_PASSWORD", ""),
			DB:       getInt("OFO_REDIS_DB", 0),
		},
		Worker: WorkerConfig{
			Concurrency:  getInt("OFO_WORKER_CONCURRENCY", 4),
			PollInterval: getDur("OFO_WORKER_POLL_INTERVAL", time.Second),
			ClaimBatch:   getInt("OFO_WORKER_CLAIM_BATCH", 10),
			LockTTL:      getDur("OFO_WORKER_LOCK_TTL", 30*time.Second),
			ReapInterval: getDur("OFO_WORKER_REAP_INTERVAL", 15*time.Second),
		},
		Observability: ObservabilityConfig{
			OTelEnabled:  getBool("OFO_OTEL_ENABLED", true),
			OTLPEndpoint: getStr("OFO_OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4318"),
			ServiceName:  getStr("OFO_OTEL_SERVICE_NAME", "order-fulfillment-orchestrator"),
			MetricsAddr:  getStr("OFO_METRICS_ADDR", ":9090"),
			LogLevel:     getStr("OFO_LOG_LEVEL", "info"),
		},
		Sim: SimConfig{
			PaymentDeclineRate:     getFloat("OFO_SIM_PAYMENT_DECLINE_RATE", 0),
			FulfillmentFailureRate: getFloat("OFO_SIM_FULFILLMENT_FAILURE_RATE", 0),
		},
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if c.DB.Host == "" || c.DB.Name == "" {
		return fmt.Errorf("database host and name are required")
	}
	if c.Worker.Concurrency < 1 {
		return fmt.Errorf("worker concurrency must be >= 1")
	}
	if c.Sim.PaymentDeclineRate < 0 || c.Sim.PaymentDeclineRate > 1 {
		return fmt.Errorf("OFO_SIM_PAYMENT_DECLINE_RATE must be between 0 and 1")
	}
	if c.Sim.FulfillmentFailureRate < 0 || c.Sim.FulfillmentFailureRate > 1 {
		return fmt.Errorf("OFO_SIM_FULFILLMENT_FAILURE_RATE must be between 0 and 1")
	}
	return nil
}

func getStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func getFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getDur(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
