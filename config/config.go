package config

import (
	"log"
	"os"
	"strconv"

	"github.com/calionauta/cali-go-stack/internal/secrets"
)

type Config struct {
	Host     string
	Port     int
	LogLevel string
	Dev      bool

	DBPath        string
	DataDir       string
	EncryptionKey string

	NATS struct {
		Enabled  bool
		StoreDir string
	}

	Workflow struct {
		Enabled    bool
		DataDir    string
		ExecutorID string
	}

	GoAI struct {
		APIKey string
	}
}

func Load() *Config {
	secrets.Load()

	dev := os.Getenv("ENVIRONMENT") != "production"

	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			log.Printf("config: invalid PORT=%q, using %d: %v", p, port, err)
		} else {
			port = parsed
		}
	}

	cfg := &Config{
		Host:          getEnv("HOST", "0.0.0.0"),
		Port:          port,
		LogLevel:      getEnv("LOG_LEVEL", "INFO"),
		Dev:           dev,
		DBPath:        getEnv("DATABASE_PATH", "data/app.db"),
		DataDir:       getEnv("DATA_DIR", "data"),
		EncryptionKey: os.Getenv("ENCRYPTION_KEY"),
		GoAI: struct{ APIKey string }{
			APIKey: os.Getenv("GOAI_API_KEY"),
		},
	}

	if os.Getenv("NATS_ENABLED") == "true" {
		cfg.NATS.Enabled = true
		cfg.NATS.StoreDir = getEnv("NATS_STORE_DIR", "data/nats")
	}

	if os.Getenv("WORKFLOW_ENABLED") == "true" {
		cfg.Workflow.Enabled = true
		cfg.Workflow.DataDir = getEnv("WORKFLOW_DATA_DIR", cfg.DataDir+"/workflow")
		cfg.Workflow.ExecutorID = getEnv("WORKFLOW_EXECUTOR_ID", "local")
	}

	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
