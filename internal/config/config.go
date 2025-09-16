package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config содержит конфигурацию приложения, получаемую из окружения.
type Config struct {
	BotToken         string
	AlertSymbols     []string
	ThresholdPercent float64
	PollIntervalSec  int
	AlertChatID      int64
	LogLevel         string
}

// Load загружает конфигурацию из переменных окружения.
func Load() (Config, error) {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return Config{}, fmt.Errorf("BOT_TOKEN is not set")
	}

	// ALERT_SYMBOLS: comma-separated CoinGecko IDs
	symbolsEnv := os.Getenv("ALERT_SYMBOLS")
	var symbols []string
	if symbolsEnv != "" {
		for _, s := range strings.Split(symbolsEnv, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				symbols = append(symbols, s)
			}
		}
	}

	// THRESHOLD_PERCENT: float (поддержка запятой как десятичного разделителя)
	threshold := 2.0
	if v := os.Getenv("THRESHOLD_PERCENT"); v != "" {
		v = strings.ReplaceAll(v, ",", ".")
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			threshold = f
		}
	}

	// POLL_INTERVAL_SEC: int
	pollSec := 30
	if v := os.Getenv("POLL_INTERVAL_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			pollSec = n
		}
	}

	// ALERT_CHAT_ID: int64
	var alertChatID int64
	if v := os.Getenv("ALERT_CHAT_ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			alertChatID = n
		}
	}

	// LOG_LEVEL: string (debug, info, warn, error)
	logLevel := "info"
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "debug" || v == "info" || v == "warn" || v == "error" {
			logLevel = v
		}
	}

	return Config{
		BotToken:         token,
		AlertSymbols:     symbols,
		ThresholdPercent: threshold,
		PollIntervalSec:  pollSec,
		AlertChatID:      alertChatID,
		LogLevel:         logLevel,
	}, nil
}
