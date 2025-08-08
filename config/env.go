package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

var (
	GlobalAppConfig *AppConfig
)

type AppConfig struct {
	RPC        string
	WSRPC      string
	BatchSize  int
	DBhost     string
	DBport     int
	DBuser     string
	DBpassword string
	DBname     string
	DBsslmode  string
}

func Load() {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found, using environment variables")
	}

	GlobalAppConfig = &AppConfig{
		RPC:        getEnv("RPC", ""),
		WSRPC:      getEnv("WSRPC", ""),
		BatchSize:  getIntEnv("BATCH_SIZE", 100),
		DBport:     getIntEnv("DB_PORT", 5432),
		DBhost:     getEnv("DB_HOST", ""),
		DBuser:     getEnv("DB_USER", ""),
		DBpassword: getEnv("DB_PASSWORD", ""),
		DBname:     getEnv("DB_NAME", ""),
		DBsslmode:  getEnv("DB_SSLMODE", ""),
	}
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func getIntEnv(key string, defaultValue int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		log.Printf("Invalid integer value for %s, using default: %d\n", key, defaultValue)
		return defaultValue
	}
	return value
}
