package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config содержит конфигурацию приложения, получаемую из окружения.
type Config struct {
	BotToken               string
	LogLevel               string
	SharpChangePercent     float64 // Процент для алертов о резких изменениях
	SharpChangeIntervalMin int     // Интервал в минутах для проверки резких изменений
	DatabasePath           string  // Путь к файлу базы данных SQLite
}

// Load загружает конфигурацию из переменных окружения.
func Load() (Config, error) {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return Config{}, fmt.Errorf("BOT_TOKEN is not set")
	}

	// LOG_LEVEL: string (debug, info, warn, error)
	logLevel := "info"
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "debug" || v == "info" || v == "warn" || v == "error" {
			logLevel = v
		}
	}

	// SHARP_CHANGE_PERCENT: процент для алертов о резких изменениях (по умолчанию 10%)
	sharpChangePercent := 0.2
	if v := os.Getenv("SHARP_CHANGE_PERCENT"); v != "" {
		v = strings.ReplaceAll(v, ",", ".")
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			sharpChangePercent = f
		}
	}

	// SHARP_CHANGE_INTERVAL_MIN: интервал в минутах для проверки резких изменений (по умолчанию 15 минут)
	sharpChangeIntervalMin := 15
	if v := os.Getenv("SHARP_CHANGE_INTERVAL_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sharpChangeIntervalMin = n
		}
	}

	// DATABASE_PATH: путь к файлу базы данных SQLite (по умолчанию data/alerts.db)
	databasePath := "data/alerts.db"
	if v := os.Getenv("DATABASE_PATH"); v != "" {
		databasePath = strings.TrimSpace(v)
	}

	return Config{
		BotToken:               token,
		LogLevel:               logLevel,
		SharpChangePercent:     sharpChangePercent,
		SharpChangeIntervalMin: sharpChangeIntervalMin,
		DatabasePath:           databasePath,
	}, nil
}
