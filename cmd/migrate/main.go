package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"

	"example.com/alert-bot/internal/alerts"
)

// OldAlert структура для чтения из JSON файла
type OldAlert struct {
	ID            string    `json:"id"`
	ChatID        int64     `json:"chat_id"`
	Symbol        string    `json:"symbol"`
	TargetPrice   float64   `json:"target_price,omitempty"`
	TargetPercent float64   `json:"target_percent,omitempty"`
	BasePrice     float64   `json:"base_price,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func main() {
	logrus.SetLevel(logrus.InfoLevel)

	jsonPath := "data/alerts.json"
	dbPath := "data/alerts.db"

	// Проверяем, существует ли JSON файл
	if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
		logrus.Info("alerts.json not found, no migration needed")
		return
	}

	// Проверяем, не существует ли уже БД
	if _, err := os.Stat(dbPath); err == nil {
		logrus.Info("database already exists, migration skipped")
		return
	}

	logrus.Info("starting migration from JSON to SQLite")

	// Создаем директорию для БД если не существует
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		logrus.Fatalf("failed to create data directory: %v", err)
	}

	// Читаем JSON файл
	file, err := os.Open(jsonPath)
	if err != nil {
		logrus.Fatalf("failed to open alerts.json: %v", err)
	}
	defer file.Close()

	var oldAlerts []OldAlert
	if err := json.NewDecoder(file).Decode(&oldAlerts); err != nil {
		logrus.Fatalf("failed to decode alerts.json: %v", err)
	}

	logrus.WithField("count", len(oldAlerts)).Info("loaded alerts from JSON")

	// Создаем новое хранилище
	storage, err := alerts.NewDatabaseStorage(dbPath)
	if err != nil {
		logrus.Fatalf("failed to create database storage: %v", err)
	}
	defer storage.Close()

	// Мигрируем данные
	migrated := 0
	for _, oldAlert := range oldAlerts {
		newAlert := alerts.Alert{
			ID:            oldAlert.ID,
			ChatID:        oldAlert.ChatID,
			Symbol:        oldAlert.Symbol,
			TargetPrice:   oldAlert.TargetPrice,
			TargetPercent: oldAlert.TargetPercent,
			BasePrice:     oldAlert.BasePrice,
			CreatedAt:     oldAlert.CreatedAt,
		}

		_, err := storage.Add(newAlert)
		if err != nil {
			logrus.WithError(err).WithField("alert_id", oldAlert.ID).Warn("failed to migrate alert")
			continue
		}
		migrated++
	}

	logrus.WithField("migrated", migrated).Info("migration completed successfully")

	// Создаем backup старого файла
	backupPath := jsonPath + ".backup." + time.Now().Format("20060102-150405")
	if err := os.Rename(jsonPath, backupPath); err != nil {
		logrus.WithError(err).Warn("failed to backup old alerts.json")
	} else {
		logrus.WithField("backup_path", backupPath).Info("old alerts.json backed up")
	}

	fmt.Printf("Migration completed: %d alerts migrated to %s\n", migrated, dbPath)
	fmt.Printf("Old file backed up as: %s\n", backupPath)
}
