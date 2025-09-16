package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"

	internalbot "example.com/alert-bot/internal/bot"
	"example.com/alert-bot/internal/config"
)

func main() {
	// Загружаем .env в самом начале
	if err := godotenv.Load(); err != nil {
		logrus.WithError(err).Warn("failed to load .env file")
	}

	// Настройка логгера после загрузки .env
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	logrus.SetLevel(logrus.InfoLevel)

	cfg, err := config.Load()
	if err != nil {
		logrus.Fatalf("config load error: %v", err)
	}

	// Установка уровня логирования из конфигурации
	switch cfg.LogLevel {
	case "debug":
		logrus.SetLevel(logrus.DebugLevel)
	case "warn":
		logrus.SetLevel(logrus.WarnLevel)
	case "error":
		logrus.SetLevel(logrus.ErrorLevel)
	default:
		logrus.SetLevel(logrus.InfoLevel)
	}

	logrus.WithFields(logrus.Fields{
		"log_level":         cfg.LogLevel,
		"alert_symbols":     cfg.AlertSymbols,
		"threshold_percent": cfg.ThresholdPercent,
		"poll_interval_sec": cfg.PollIntervalSec,
		"alert_chat_id":     cfg.AlertChatID,
	}).Info("config loaded")

	bot, err := internalbot.NewTelegramBot(cfg)
	if err != nil {
		logrus.Fatalf("bot init error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Обработка сигналов для graceful shutdown
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-signals
		logrus.Info("shutdown signal received")
		cancel()
	}()

	if err := bot.Start(ctx); err != nil {
		logrus.Fatalf("bot run error: %v", err)
	}

	// Даем немного времени на корректное завершение внутренних горутин
	time.Sleep(300 * time.Millisecond)
	logrus.Info("bot stopped")
}
