package alerts

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"example.com/alert-bot/internal/reminder"
	"github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
	// Возвращаем pure Go SQLite драйвер
)

type Alert struct {
	ID            string    `json:"id"`
	ChatID        int64     `json:"chat_id"`
	UserID        int64     `json:"user_id"`  // ID пользователя Telegram
	Username      string    `json:"username"` // Username пользователя Telegram
	Symbol        string    `json:"symbol"`
	Market        string    `json:"market"`   // "spot" или "futures"
	Exchange      string    `json:"exchange"` // "Bitget" или "Bybit"
	TargetPrice   float64   `json:"target_price,omitempty"`
	TargetPercent float64   `json:"target_percent,omitempty"`
	BasePrice     float64   `json:"base_price,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Call struct {
	ID             string     `json:"id"`
	UserID         int64      `json:"user_id"`
	Username       string     `json:"username"`
	ChatID         int64      `json:"chat_id"`
	Symbol         string     `json:"symbol"`
	Market         string     `json:"market"`    // "spot" или "futures"
	Exchange       string     `json:"exchange"`  // "Bitget" или "Bybit"
	Direction      string     `json:"direction"` // "long" или "short"
	EntryPrice     float64    `json:"entry_price"`
	Size           float64    `json:"size"`                      // Размер позиции (от 0 до 100) - это не процент от депозита, а скорее абстрактный "объем" позиции
	DepositPercent float64    `json:"deposit_percent,omitempty"` // Процент от депозита, задействованный в сделке
	ExitPrice      float64    `json:"exit_price,omitempty"`
	PnlPercent     float64    `json:"pnl_percent,omitempty"`
	Status         string     `json:"status"` // "open" или "closed"
	OpenedAt       time.Time  `json:"opened_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	StopLossPrice  float64    `json:"stop_loss_price,omitempty"` // Цена стоп-лосса
}

type AlertTrigger struct {
	ID           int64     `json:"id"`
	AlertID      string    `json:"alert_id"`
	Symbol       string    `json:"symbol"`
	TriggerPrice float64   `json:"trigger_price"`
	ChatID       int64     `json:"chat_id"`
	UserID       int64     `json:"user_id"`
	Username     string    `json:"username"`
	TriggerType  string    `json:"trigger_type"` // "price", "percent", "sharp_change"
	TriggeredAt  time.Time `json:"triggered_at"`
}

type PriceHistory struct {
	ID        int64     `json:"id"`
	Symbol    string    `json:"symbol"`
	Price     float64   `json:"price"`
	Timestamp time.Time `json:"timestamp"`
}

type UserStats struct {
	UserID                    int64   `json:"user_id"`
	Username                  string  `json:"username"`
	TotalCalls                int     `json:"total_calls"`
	ClosedCalls               int     `json:"closed_calls"`
	WinningCalls              int     `json:"winning_calls"`
	TotalPnl                  float64 `json:"total_pnl"`
	AveragePnl                float64 `json:"average_pnl"`
	WinRate                   float64 `json:"win_rate"`
	BestCall                  float64 `json:"best_call"`
	WorstCall                 float64 `json:"worst_call"`
	TotalActiveDepositPercent float64 `json:"total_active_deposit_percent"`
	TotalPnlToDeposit         float64 `json:"total_pnl_to_deposit"`
	InitialDeposit            float64 `json:"initial_deposit"`
	CurrentDeposit            float64 `json:"current_deposit"`
	TotalReturnPercent        float64 `json:"total_return_percent"`
}

type DatabaseStorage struct {
	db *sql.DB
}

func generateShortID() string {
	bytes := make([]byte, 4) // 4 байта = 8 hex символов
	if _, err := rand.Read(bytes); err != nil {
		return hex.EncodeToString([]byte{byte(time.Now().Unix())})[:8]
	}
	return hex.EncodeToString(bytes)
}

func NewDatabaseStorage(dbPath string) (*DatabaseStorage, error) {
	if dbPath == "" {
		dbPath = "data/alerts.db"
	}

	db, err := sql.Open("sqlite", dbPath+"?_foreign_keys=on") // Возвращаем драйвер "sqlite"
	if err != nil {
		return nil, err
	}

	storage := &DatabaseStorage{db: db}
	if err := storage.migrate(); err != nil {
		return nil, err
	}

	logrus.WithField("db_path", dbPath).Info("database storage initialized")
	return storage, nil
}

func (s *DatabaseStorage) Close() error {
	logrus.Info("closing database connection")
	return s.db.Close()
}

// DB возвращает *sql.DB для внешних пакетов
func (s *DatabaseStorage) DB() *sql.DB {
	return s.db
}
func (s *DatabaseStorage) InsertReminder(r reminder.Task) error {
	return reminder.InsertReminder(s.db, r)
}
func (s *DatabaseStorage) DeleteReminder(id string) { reminder.DeleteReminder(s.db, id) }
func (s *DatabaseStorage) GetPendingReminders() ([]reminder.Task, error) {
	return reminder.GetPendingReminders(s.db)
}

func (s *DatabaseStorage) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS reminders (
			id TEXT PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			username TEXT DEFAULT '',
			symbol TEXT NOT NULL,
			text TEXT DEFAULT '',
			trigger_at DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_reminders_trigger_at ON reminders(trigger_at)`,
		`CREATE INDEX IF NOT EXISTS idx_reminders_chat_id   ON reminders(chat_id)`,
		`CREATE TABLE IF NOT EXISTS alerts (
			id TEXT PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL DEFAULT 0,
			username TEXT DEFAULT '',
			symbol TEXT NOT NULL,
			target_price REAL DEFAULT 0,
			target_percent REAL DEFAULT 0,
			base_price REAL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			market TEXT DEFAULT '',
			exchange TEXT DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS user_deposits (
    		user_id INTEGER PRIMARY KEY,
    		initial_deposit REAL DEFAULT 100,
    		current_deposit REAL DEFAULT 100,
    		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_deposits_user_id ON user_deposits(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_chat_id ON alerts(chat_id)`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_user_id ON alerts(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_symbol ON alerts(symbol)`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_created_at ON alerts(created_at)`,

		`CREATE TABLE IF NOT EXISTS calls (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			username TEXT NOT NULL,
			chat_id INTEGER NOT NULL,
			symbol TEXT NOT NULL,
			direction TEXT NOT NULL DEFAULT 'long',
			entry_price REAL NOT NULL,
			exit_price REAL DEFAULT 0,
			pnl_percent REAL DEFAULT 0,
			size REAL DEFAULT 100,
			status TEXT NOT NULL DEFAULT 'open',
			opened_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			closed_at DATETIME,
			market TEXT DEFAULT '',
			deposit_percent REAL DEFAULT 0,
			stop_loss_price REAL DEFAULT 0,
			exchange TEXT DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_calls_user_id ON calls(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_calls_status ON calls(status)`,
		`CREATE INDEX IF NOT EXISTS idx_calls_symbol ON calls(symbol)`,
		`CREATE INDEX IF NOT EXISTS idx_calls_opened_at ON calls(opened_at)`,

		`CREATE TABLE IF NOT EXISTS alert_triggers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			alert_id TEXT,
			symbol TEXT NOT NULL,
			trigger_price REAL NOT NULL,
			chat_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL DEFAULT 0,
			username TEXT DEFAULT '',
			trigger_type TEXT NOT NULL,
			triggered_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_triggers_symbol ON alert_triggers(symbol)`,
		`CREATE INDEX IF NOT EXISTS idx_triggers_chat_id ON alert_triggers(chat_id)`,
		`CREATE INDEX IF NOT EXISTS idx_triggers_user_id ON alert_triggers(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_triggers_triggered_at ON alert_triggers(triggered_at)`,

		`CREATE TABLE IF NOT EXISTS price_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL,
			price REAL NOT NULL,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_price_history_symbol ON price_history(symbol)`,
		`CREATE INDEX IF NOT EXISTS idx_price_history_timestamp ON price_history(timestamp)`,

		// Миграция существующих данных - добавляем колонки если их нет
		`ALTER TABLE alerts ADD COLUMN user_id INTEGER DEFAULT 0`,
		`ALTER TABLE alerts ADD COLUMN username TEXT DEFAULT ''`,
		`ALTER TABLE alerts ADD COLUMN market TEXT DEFAULT ''`,
		`ALTER TABLE alerts ADD COLUMN exchange TEXT DEFAULT ''`,
		`ALTER TABLE alert_triggers ADD COLUMN user_id INTEGER DEFAULT 0`,
		`ALTER TABLE alert_triggers ADD COLUMN username TEXT DEFAULT ''`,
		`ALTER TABLE calls ADD COLUMN market TEXT DEFAULT ''`,
		`ALTER TABLE calls ADD COLUMN exchange TEXT DEFAULT ''`,
		`ALTER TABLE calls ADD COLUMN size REAL DEFAULT 100`,
		`ALTER TABLE calls ADD COLUMN deposit_percent REAL DEFAULT 0`,
		`ALTER TABLE calls ADD COLUMN stop_loss_price REAL DEFAULT 0`,
	}
	// Обновляем старые коллы без size
	_, err := s.db.Exec(`UPDATE calls SET size = 100 WHERE size IS NULL OR size = 0`)
	if err != nil {
		logrus.WithError(err).Warn("failed to update old calls with default size")
	}
	for _, query := range queries {
		if _, err := s.db.Exec(query); err != nil {
			// Игнорируем ошибки добавления колонок если они уже существуют
			if !strings.Contains(err.Error(), "duplicate column name") {
				logrus.WithError(err).WithField("query", query).Warn("migration query failed")
			}
		}
	}

	logrus.Info("database migration completed")
	return nil
}

func (s *DatabaseStorage) Add(alert Alert) (Alert, error) {
	if alert.ID == "" {
		// Генерируем уникальный короткий ID
		for {
			alert.ID = generateShortID()
			var exists bool
			err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM alerts WHERE id = ?)", alert.ID).Scan(&exists)
			if err != nil {
				return alert, err
			}
			if !exists {
				break
			}
		}
	}

	if alert.CreatedAt.IsZero() {
		alert.CreatedAt = time.Now()
	}

	_, err := s.db.Exec(`
		INSERT INTO alerts (id, chat_id, user_id, username, symbol, market, target_price, target_percent, base_price, created_at, exchange)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		alert.ID, alert.ChatID, alert.UserID, alert.Username, alert.Symbol, alert.Market,
		alert.TargetPrice, alert.TargetPercent, alert.BasePrice, alert.CreatedAt, alert.Exchange)

	if err != nil {
		return alert, err
	}

	logrus.WithFields(logrus.Fields{
		"alert_id": alert.ID,
		"chat_id":  alert.ChatID,
		"user_id":  alert.UserID,
		"username": alert.Username,
		"symbol":   alert.Symbol,
	}).Debug("alert added to database")

	return alert, nil
}

func (s *DatabaseStorage) Update(alert Alert) error {
	if alert.ID == "" {
		return errors.New("alert id is empty")
	}

	_, err := s.db.Exec(`
		UPDATE alerts 
		SET chat_id = ?, user_id = ?, username = ?, symbol = ?, market = ?, target_price = ?, target_percent = ?, base_price = ?, exchange = ?
		WHERE id = ?`,
		alert.ChatID, alert.UserID, alert.Username, alert.Symbol, alert.Market,
		alert.TargetPrice, alert.TargetPercent, alert.BasePrice, alert.Exchange, alert.ID)

	return err
}

// GetUserDeposit получает информацию о депозите пользователя
func (s *DatabaseStorage) GetUserDeposit(userID int64) (initialDeposit, currentDeposit float64, err error) {
	err = s.db.QueryRow(`
		SELECT initial_deposit, current_deposit 
		FROM user_deposits 
		WHERE user_id = ?`, userID).Scan(&initialDeposit, &currentDeposit)

	if err == sql.ErrNoRows {
		// Если депозит не найден, создаем новый с начальным значением 100
		_, err = s.db.Exec(`
			INSERT INTO user_deposits (user_id, initial_deposit, current_deposit) 
			VALUES (?, 100, 100)`, userID)
		if err != nil {
			return 0, 0, err
		}
		return 100, 100, nil
	}

	return initialDeposit, currentDeposit, err
}

// UpdateUserDeposit обновляет текущий депозит пользователя
func (s *DatabaseStorage) UpdateUserDeposit(userID int64, newDeposit float64) error {
	// Сначала проверяем, существует ли запись
	var exists bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM user_deposits WHERE user_id = ?)`, userID).Scan(&exists)
	if err != nil {
		return err
	}

	if !exists {
		// Создаем запись, если её нет
		_, err = s.db.Exec(`
			INSERT INTO user_deposits (user_id, initial_deposit, current_deposit) 
			VALUES (?, 100, ?)`, userID, newDeposit)
	} else {
		// Обновляем существующую
		_, err = s.db.Exec(`
			UPDATE user_deposits 
			SET current_deposit = ?, updated_at = CURRENT_TIMESTAMP 
			WHERE user_id = ?`, newDeposit, userID)
	}

	if err != nil {
		return err
	}

	logrus.WithFields(logrus.Fields{
		"user_id":         userID,
		"current_deposit": newDeposit,
	}).Debug("user deposit updated")

	return nil
}

// ResetUserDeposit сбрасывает депозит пользователя до начального значения
func (s *DatabaseStorage) ResetUserDeposit(userID int64) error {
	_, err := s.db.Exec(`
		UPDATE user_deposits 
		SET current_deposit = initial_deposit, updated_at = CURRENT_TIMESTAMP 
		WHERE user_id = ?`, userID)

	return err
}
func (s *DatabaseStorage) ListByChat(chatID int64) []Alert {
	rows, err := s.db.Query(`
		SELECT id, chat_id, COALESCE(user_id, 0), COALESCE(username, ''), symbol, market, target_price, target_percent, base_price, created_at, exchange
		FROM alerts 
		WHERE chat_id = ?
		ORDER BY created_at ASC`, chatID)

	if err != nil {
		logrus.WithError(err).Warn("failed to list alerts by chat")
		return nil
	}
	defer rows.Close()

	var alerts []Alert
	for rows.Next() {
		var alert Alert
		err := rows.Scan(&alert.ID, &alert.ChatID, &alert.UserID, &alert.Username, &alert.Symbol, &alert.Market,
			&alert.TargetPrice, &alert.TargetPercent, &alert.BasePrice, &alert.CreatedAt, &alert.Exchange)
		if err != nil {
			logrus.WithError(err).Warn("failed to scan alert row")
			continue
		}
		alerts = append(alerts, alert)
	}

	return alerts
}

func (s *DatabaseStorage) DeleteByID(chatID int64, id string) (bool, error) {
	result, err := s.db.Exec("DELETE FROM alerts WHERE id = ? AND chat_id = ?", id, chatID)
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	deleted := affected > 0
	if deleted {
		logrus.WithFields(logrus.Fields{
			"alert_id": id,
			"chat_id":  chatID,
		}).Debug("alert deleted from database")
	}

	return deleted, nil
}

func (s *DatabaseStorage) DeleteAllByChat(chatID int64) (int, error) {
	result, err := s.db.Exec("DELETE FROM alerts WHERE chat_id = ?", chatID)
	if err != nil {
		return 0, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	count := int(affected)
	if count > 0 {
		logrus.WithFields(logrus.Fields{
			"chat_id": chatID,
			"count":   count,
		}).Debug("alerts deleted from database")
	}

	return count, nil
}

func (s *DatabaseStorage) GetBySymbol(symbol string) []Alert {
	rows, err := s.db.Query(`
		SELECT id, chat_id, COALESCE(user_id, 0), COALESCE(username, ''), symbol, market, target_price, target_percent, base_price, created_at, exchange
		FROM alerts 
		WHERE symbol = ?`, symbol)

	if err != nil {
		logrus.WithError(err).Warn("failed to get alerts by symbol")
		return nil
	}
	defer rows.Close()

	var alerts []Alert
	for rows.Next() {
		var alert Alert
		err := rows.Scan(&alert.ID, &alert.ChatID, &alert.UserID, &alert.Username, &alert.Symbol, &alert.Market,
			&alert.TargetPrice, &alert.TargetPercent, &alert.BasePrice, &alert.CreatedAt, &alert.Exchange)
		if err != nil {
			logrus.WithError(err).Warn("failed to scan alert row")
			continue
		}
		alerts = append(alerts, alert)
	}

	return alerts
}

func (s *DatabaseStorage) GetAllSymbols() []string {
	// Получаем символы из алертов и открытых коллов
	rows, err := s.db.Query(`
		SELECT DISTINCT symbol FROM (
			SELECT symbol FROM alerts WHERE symbol != ''
			UNION
			SELECT symbol FROM calls WHERE symbol != '' AND status = 'open'
		) ORDER BY symbol`)

	if err != nil {
		logrus.WithError(err).Warn("failed to get all symbols")
		return nil
	}
	defer rows.Close()

	var symbols []string
	for rows.Next() {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			logrus.WithError(err).Warn("failed to scan symbol")
			continue
		}
		symbols = append(symbols, symbol)
	}

	logrus.WithField("symbols", symbols).Debug("retrieved symbols from alerts and open calls")
	return symbols
}

func (s *DatabaseStorage) GetSymbolsFromUserAlertsAndCalls(chatID int64) []string {
	// Получаем символы из алертов и открытых коллов конкретного пользователя
	rows, err := s.db.Query(`
		SELECT DISTINCT symbol FROM (
			SELECT symbol FROM alerts WHERE chat_id = ? AND symbol != ''
			UNION
			SELECT symbol FROM calls WHERE chat_id = ? AND symbol != '' AND status = 'open'
		) ORDER BY symbol`, chatID, chatID)

	if err != nil {
		logrus.WithError(err).Warn("failed to get user symbols")
		return nil
	}
	defer rows.Close()

	var symbols []string
	for rows.Next() {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			logrus.WithError(err).Warn("failed to scan user symbol")
			continue
		}
		symbols = append(symbols, symbol)
	}

	return symbols
}

// Методы для работы с коллами

func (s *DatabaseStorage) OpenCall(call Call) (Call, error) {
	if call.ID == "" {
		// Генерируем уникальный короткий ID
		for {
			call.ID = generateShortID()
			var exists bool
			err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM calls WHERE id = ?)", call.ID).Scan(&exists)
			if err != nil {
				return call, err
			}
			if !exists {
				break
			}
		}
	}

	if call.OpenedAt.IsZero() {
		call.OpenedAt = time.Now()
	}

	if call.Direction == "" {
		call.Direction = "long"
	}

	call.Status = "open"
	call.Size = 100.0 // Инициализируем размер позиции по умолчанию

	_, err := s.db.Exec(`
		INSERT INTO calls (id, user_id, username, chat_id, symbol, market, direction, entry_price, size, status, opened_at, deposit_percent, stop_loss_price, exchange)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		call.ID, call.UserID, call.Username, call.ChatID, call.Symbol, call.Market,
		call.Direction, call.EntryPrice, call.Size, call.Status, call.OpenedAt, call.DepositPercent, call.StopLossPrice, call.Exchange)

	if err != nil {
		return call, err
	}

	logrus.WithFields(logrus.Fields{
		"call_id":       call.ID,
		"user_id":       call.UserID,
		"username":      call.Username,
		"symbol":        call.Symbol,
		"direction":     call.Direction,
		"entry_price":   call.EntryPrice,
		"position_size": call.DepositPercent,
	}).Info("call opened")

	return call, nil
}

func (s *DatabaseStorage) CloseCall(callID string, userID int64, exitPrice float64, sizeToClose float64) error {
	// Получаем информацию о колле
	var call Call
	err := s.db.QueryRow(`
		SELECT id, user_id, username, chat_id, symbol, market, direction, entry_price, size, status, deposit_percent
		FROM calls WHERE id = ? AND user_id = ? AND status = 'open'`,
		callID, userID).Scan(
		&call.ID, &call.UserID, &call.Username, &call.ChatID,
		&call.Symbol, &call.Market, &call.Direction, &call.EntryPrice, &call.Size, &call.Status, &call.DepositPercent)

	if err != nil {
		if err == sql.ErrNoRows {
			return errors.New("call not found or already closed")
		}
		return err
	}

	// Проверяем, что запрошенный размер не превышает текущий
	if sizeToClose <= 0 || sizeToClose > call.Size {
		return fmt.Errorf("неверный размер для закрытия. Должен быть от 0 до текущего размера %.2f", call.Size)
	}

	// Вычисляем базовый PnL (изменение цены в процентах)
	var basePnlPercent float64
	if call.Direction == "long" {
		basePnlPercent = ((exitPrice - call.EntryPrice) / call.EntryPrice) * 100
	} else { // short
		basePnlPercent = ((call.EntryPrice - exitPrice) / call.EntryPrice) * 100
	}

	// PnL для закрываемой части позиции
	// Размер позиции учитывается в изменении депозита
	pnlPercentForClosedPart := basePnlPercent

	// Рассчитываем изменение депозита
	if call.DepositPercent > 0 {
		_, currentDeposit, err := s.GetUserDeposit(userID)
		if err != nil {
			logrus.WithError(err).Warn("failed to get user deposit for PnL calculation")
		} else {
			// Вычисляем, какая часть позиции закрывается
			closedPositionPercent := call.DepositPercent * (sizeToClose / call.Size)

			// Изменение депозита = размер_позиции × изменение_цены
			// Например: позиция 200%, цена +10% → депозит +20%
			depositChangePercent := closedPositionPercent * (basePnlPercent / 100)
			depositChange := (depositChangePercent / 100) * currentDeposit

			newDeposit := currentDeposit + depositChange

			// Обновляем депозит пользователя
			err = s.UpdateUserDeposit(userID, newDeposit)
			if err != nil {
				logrus.WithError(err).Warn("failed to update user deposit after closing call")
			} else {
				logrus.WithFields(logrus.Fields{
					"user_id":               userID,
					"call_id":               callID,
					"closed_position_pct":   closedPositionPercent,
					"base_pnl_pct":          basePnlPercent,
					"deposit_change_pct":    depositChangePercent,
					"deposit_change_amount": depositChange,
					"old_deposit":           currentDeposit,
					"new_deposit":           newDeposit,
				}).Info("user deposit updated after call close")
			}
		}
	}

	newSize := call.Size - sizeToClose
	status := "open"
	var closedAt sql.NullTime

	// Если оставшийся размер очень мал, считаем колл полностью закрытым
	if newSize < 0.001 {
		status = "closed"
		now := time.Now()
		closedAt = sql.NullTime{Time: now, Valid: true}
		newSize = 0.0
		//newDepositPercent = 0.0
	}

	// Обновляем колл в базе данных
	_, err = s.db.Exec(`
		UPDATE calls
		SET exit_price = ?, pnl_percent = ?, size = ?, status = ?, closed_at = ?
		WHERE id = ?`,
		exitPrice, pnlPercentForClosedPart, newSize, status, closedAt, callID)

	if err != nil {
		return err
	}

	logrus.WithFields(logrus.Fields{
		"call_id":     callID,
		"user_id":     userID,
		"username":    call.Username,
		"symbol":      call.Symbol,
		"direction":   call.Direction,
		"entry_price": call.EntryPrice,
		"exit_price":  exitPrice,
		"pnl_percent": pnlPercentForClosedPart,
		"closed_size": sizeToClose,
		"new_size":    newSize,
		"status":      status,
	}).Info("call closed (partially or fully)")

	return nil
}

func (s *DatabaseStorage) UpdateStopLoss(callID string, userID int64, stopLossPrice float64) error {
	result, err := s.db.Exec(`
		UPDATE calls
		SET stop_loss_price = ?
		WHERE id = ? AND user_id = ? AND status = 'open'`,
		stopLossPrice, callID, userID)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("call not found or already closed")
	}

	logrus.WithFields(logrus.Fields{
		"call_id":         callID,
		"user_id":         userID,
		"stop_loss_price": stopLossPrice,
	}).Info("stop-loss updated")

	return nil
}

func (s *DatabaseStorage) GetUserCalls(userID int64, onlyOpen bool) []Call {
	query := `
		SELECT id, user_id, username, chat_id, symbol, market, direction, entry_price, size, 
		       COALESCE(exit_price, 0), COALESCE(pnl_percent, 0), status, opened_at, closed_at, COALESCE(deposit_percent, 0), COALESCE(stop_loss_price, 0), exchange
		FROM calls 
		WHERE user_id = ?`

	if onlyOpen {
		query += " AND status = 'open'"
	}

	query += " ORDER BY opened_at DESC"

	rows, err := s.db.Query(query, userID)
	if err != nil {
		logrus.WithError(err).Warn("failed to get user calls")
		return nil
	}
	defer rows.Close()

	var calls []Call
	for rows.Next() {
		var call Call
		var closedAt sql.NullTime
		err := rows.Scan(&call.ID, &call.UserID, &call.Username, &call.ChatID,
			&call.Symbol, &call.Market, &call.Direction, &call.EntryPrice, &call.Size, &call.ExitPrice,
			&call.PnlPercent, &call.Status, &call.OpenedAt, &closedAt, &call.DepositPercent, &call.StopLossPrice, &call.Exchange)
		if err != nil {
			logrus.WithError(err).Warn("failed to scan call row")
			continue
		}
		if closedAt.Valid {
			call.ClosedAt = &closedAt.Time
		}
		calls = append(calls, call)
	}

	return calls
}

func (s *DatabaseStorage) GetAllOpenCalls() []Call {
	rows, err := s.db.Query(`
		SELECT id, user_id, username, chat_id, symbol, market, direction, entry_price, size, 
		       COALESCE(exit_price, 0), COALESCE(pnl_percent, 0), status, opened_at, closed_at, COALESCE(deposit_percent, 0), COALESCE(stop_loss_price, 0), exchange
		FROM calls 
		WHERE status = 'open'
		ORDER BY opened_at DESC`)

	if err != nil {
		logrus.WithError(err).Warn("failed to get all open calls")
		return nil
	}
	defer rows.Close()

	var calls []Call
	for rows.Next() {
		var call Call
		var closedAt sql.NullTime
		err := rows.Scan(&call.ID, &call.UserID, &call.Username, &call.ChatID,
			&call.Symbol, &call.Market, &call.Direction, &call.EntryPrice, &call.Size, &call.ExitPrice,
			&call.PnlPercent, &call.Status, &call.OpenedAt, &closedAt, &call.DepositPercent, &call.StopLossPrice, &call.Exchange)
		if err != nil {
			logrus.WithError(err).Warn("failed to scan call row")
			continue
		}
		if closedAt.Valid {
			call.ClosedAt = &closedAt.Time
		}
		calls = append(calls, call)
	}

	return calls
}

func (s *DatabaseStorage) GetUserStats(userID int64) (*UserStats, error) {
	var stats UserStats

	// Базовая статистика за последние 90 дней
	err := s.db.QueryRow(`
		SELECT 
			user_id,
			username,
			COUNT(*) as total_calls,
			SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END) as closed_calls,
			SUM(CASE WHEN status = 'closed' AND pnl_percent > 0 THEN 1 ELSE 0 END) as winning_calls,
			COALESCE(SUM(CASE WHEN status = 'closed' THEN pnl_percent ELSE 0 END), 0) as total_pnl,
			COALESCE(AVG(CASE WHEN status = 'closed' THEN pnl_percent ELSE NULL END), 0) as avg_pnl,
			COALESCE(MAX(CASE WHEN status = 'closed' THEN pnl_percent ELSE NULL END), 0) as best_call,
			COALESCE(MIN(CASE WHEN status = 'closed' THEN pnl_percent ELSE NULL END), 0) as worst_call
		FROM calls 
		WHERE user_id = ? AND opened_at >= datetime('now', '-90 days') and deposit_percent>0
		GROUP BY user_id, username`,
		userID).Scan(
		&stats.UserID, &stats.Username, &stats.TotalCalls, &stats.ClosedCalls,
		&stats.WinningCalls, &stats.TotalPnl, &stats.AveragePnl,
		&stats.BestCall, &stats.WorstCall)

	if err != nil {
		if err == sql.ErrNoRows {
			return &UserStats{UserID: userID}, nil
		}
		return nil, err
	}

	// Вычисляем winrate
	if stats.ClosedCalls > 0 {
		stats.WinRate = (float64(stats.WinningCalls) / float64(stats.ClosedCalls)) * 100
	}

	return &stats, nil
}

func (s *DatabaseStorage) GetAllUserStats() []UserStats {
	rows, err := s.db.Query(`
		SELECT 
			user_id,
			username,
			COUNT(*) as total_calls,
			SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END) as closed_calls,
			SUM(CASE WHEN status = 'closed' AND pnl_percent > 0 THEN 1 ELSE 0 END) as winning_calls,
			COALESCE(SUM(CASE WHEN status = 'closed' THEN pnl_percent ELSE 0 END), 0) as total_pnl,
			COALESCE(AVG(CASE WHEN status = 'closed' THEN pnl_percent ELSE NULL END), 0) as avg_pnl,
			COALESCE(MAX(CASE WHEN status = 'closed' THEN pnl_percent ELSE NULL END), 0) as best_call,
			COALESCE(MIN(CASE WHEN status = 'closed' THEN pnl_percent ELSE NULL END), 0) as worst_call
		FROM calls 
		WHERE opened_at >= datetime('now', '-90 days') and deposit_percent>0
		GROUP BY user_id, username
		ORDER BY total_pnl DESC`)

	if err != nil {
		logrus.WithError(err).Warn("failed to get all user stats")
		return nil
	}
	defer rows.Close()

	var stats []UserStats
	for rows.Next() {
		var stat UserStats
		err := rows.Scan(&stat.UserID, &stat.Username, &stat.TotalCalls, &stat.ClosedCalls,
			&stat.WinningCalls, &stat.TotalPnl, &stat.AveragePnl,
			&stat.BestCall, &stat.WorstCall)
		if err != nil {
			logrus.WithError(err).Warn("failed to scan user stats row")
			continue
		}

		// Вычисляем winrate
		if stat.ClosedCalls > 0 {
			stat.WinRate = (float64(stat.WinningCalls) / float64(stat.ClosedCalls)) * 100
		}

		// Получаем информацию о депозите
		initialDeposit, currentDeposit, err := s.GetUserDeposit(stat.UserID)
		if err == nil {
			stat.InitialDeposit = initialDeposit
			stat.CurrentDeposit = currentDeposit
			stat.TotalReturnPercent = ((currentDeposit - initialDeposit) / initialDeposit) * 100
		}

		stats = append(stats, stat)
	}

	return stats
}

func (s *DatabaseStorage) GetUserTradesBySymbol(userID int64) map[string]struct {
	TotalCalls   int
	ClosedCalls  int
	WinningCalls int
	TotalPnl     float64
	WinRate      float64
} {
	rows, err := s.db.Query(`
		SELECT 
			symbol,
			COUNT(*) as total_calls,
			SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END) as closed_calls,
			SUM(CASE WHEN status = 'closed' AND pnl_percent > 0 THEN 1 ELSE 0 END) as winning_calls,
			COALESCE(SUM(CASE WHEN status = 'closed' THEN pnl_percent ELSE 0 END), 0) as total_pnl
		FROM calls 
		WHERE user_id = ? AND opened_at >= datetime('now', '-90 days') and deposit_percent>0
		GROUP BY symbol
		ORDER BY symbol`,
		userID)

	if err != nil {
		logrus.WithError(err).Warn("failed to get user trades by symbol")
		return nil
	}
	defer rows.Close()

	result := make(map[string]struct {
		TotalCalls   int
		ClosedCalls  int
		WinningCalls int
		TotalPnl     float64
		WinRate      float64
	})

	for rows.Next() {
		var symbol string
		var totalCalls, closedCalls, winningCalls int
		var totalPnl float64

		err := rows.Scan(&symbol, &totalCalls, &closedCalls, &winningCalls, &totalPnl)
		if err != nil {
			logrus.WithError(err).Warn("failed to scan trades by symbol row")
			continue
		}

		winRate := 0.0
		if closedCalls > 0 {
			winRate = (float64(winningCalls) / float64(closedCalls)) * 100
		}

		result[symbol] = struct {
			TotalCalls   int
			ClosedCalls  int
			WinningCalls int
			TotalPnl     float64
			WinRate      float64
		}{totalCalls, closedCalls, winningCalls, totalPnl, winRate}
	}

	return result
}

func (s *DatabaseStorage) GetSymbolStats(userID int64) map[string]struct {
	ActiveAlerts  int
	TotalTriggers int
} {
	result := make(map[string]struct {
		ActiveAlerts  int
		TotalTriggers int
	})

	// Активные алерты
	rows, err := s.db.Query(`
		SELECT symbol, COUNT(*) 
		FROM alerts 
		WHERE user_id = ? 
		GROUP BY symbol`,
		userID)
	if err != nil {
		logrus.WithError(err).Warn("failed to get active alerts for symbol stats")
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var symbol string
		var count int
		if err := rows.Scan(&symbol, &count); err != nil {
			logrus.WithError(err).Warn("failed to scan active alerts for symbol stats")
			continue
		}
		stat := result[symbol]
		stat.ActiveAlerts = count
		result[symbol] = stat
	}

	// Общее количество триггеров (за последние 90 дней)
	rows, err = s.db.Query(`
		SELECT symbol, COUNT(*) 
		FROM alert_triggers 
		WHERE user_id = ? AND triggered_at >= datetime('now', '-90 days') 
		GROUP BY symbol`,
		userID)
	if err != nil {
		logrus.WithError(err).Warn("failed to get total triggers for symbol stats")
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var symbol string
		var count int
		if err := rows.Scan(&symbol, &count); err != nil {
			logrus.WithError(err).Warn("failed to scan total triggers for symbol stats")
			continue
		}
		stat := result[symbol]
		stat.TotalTriggers = count
		result[symbol] = stat
	}

	return result
}

func (s *DatabaseStorage) GetBestWorstCallsForUser(userID int64) (bestCall, worstCall *Call) {
	// Лучший колл
	var best Call
	err := s.db.QueryRow(`
		SELECT id, symbol, direction, entry_price, exit_price, pnl_percent
		FROM calls 
		WHERE user_id = ? AND status = 'closed' AND opened_at >= datetime('now', '-90 days')
		ORDER BY pnl_percent DESC LIMIT 1`,
		userID).Scan(&best.ID, &best.Symbol, &best.Direction, &best.EntryPrice, &best.ExitPrice, &best.PnlPercent)

	if err == nil {
		bestCall = &best
	}

	// Худший колл
	var worst Call
	err = s.db.QueryRow(`
		SELECT id, symbol, direction, entry_price, exit_price, pnl_percent
		FROM calls 
		WHERE user_id = ? AND status = 'closed' AND opened_at >= datetime('now', '-90 days')
		ORDER BY pnl_percent ASC LIMIT 1`,
		userID).Scan(&worst.ID, &worst.Symbol, &worst.Direction, &worst.EntryPrice, &worst.ExitPrice, &worst.PnlPercent)

	if err == nil {
		worstCall = &worst
	}

	return bestCall, worstCall
}

func (s *DatabaseStorage) GetCallByID(callID string, userID int64) (*Call, error) {
	var call Call
	var closedAt sql.NullTime

	err := s.db.QueryRow(`
		SELECT id, user_id, username, chat_id, symbol, market, direction, entry_price, size, 
		       COALESCE(exit_price, 0), COALESCE(pnl_percent, 0), status, opened_at, closed_at, COALESCE(stop_loss_price, 0), exchange
		FROM calls 
		WHERE id = ? AND user_id = ?`,
		callID, userID).Scan(
		&call.ID, &call.UserID, &call.Username, &call.ChatID,
		&call.Symbol, &call.Market, &call.Direction, &call.EntryPrice, &call.Size, &call.ExitPrice,
		&call.PnlPercent, &call.Status, &call.OpenedAt, &closedAt, &call.StopLossPrice, &call.Exchange)

	if err != nil {
		return nil, err
	}

	if closedAt.Valid {
		call.ClosedAt = &closedAt.Time
	}

	return &call, nil
}

func (s *DatabaseStorage) GetUserCallsHistory(userID int64, days int, onlyOpen bool) []Call {
	query := `
		SELECT id, user_id, username, chat_id, symbol, direction, entry_price, 
		       COALESCE(exit_price, 0), COALESCE(pnl_percent, 0), status, opened_at, closed_at
		FROM calls 
		WHERE user_id = ? AND opened_at >= datetime('now', '-' || ? || ' days')`

	if onlyOpen {
		query += " AND status = 'open'"
	}

	query += " ORDER BY opened_at DESC"

	rows, err := s.db.Query(query, userID, days)
	if err != nil {
		logrus.WithError(err).Warn("failed to get user calls history")
		return nil
	}
	defer rows.Close()

	var calls []Call
	for rows.Next() {
		var call Call
		var closedAt sql.NullTime
		err := rows.Scan(&call.ID, &call.UserID, &call.Username, &call.ChatID,
			&call.Symbol, &call.Direction, &call.EntryPrice, &call.ExitPrice,
			&call.PnlPercent, &call.Status, &call.OpenedAt, &closedAt)
		if err != nil {
			logrus.WithError(err).Warn("failed to scan call row")
			continue
		}
		if closedAt.Valid {
			call.ClosedAt = &closedAt.Time
		}
		calls = append(calls, call)
	}

	return calls
}

// Остальные методы (без изменений)

func (s *DatabaseStorage) LogAlertTrigger(alertID, symbol string, triggerPrice float64, chatID int64, userID int64, username string, triggerType string) error {
	_, err := s.db.Exec(`
		INSERT INTO alert_triggers (alert_id, symbol, trigger_price, chat_id, user_id, username, trigger_type, triggered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		alertID, symbol, triggerPrice, chatID, userID, username, triggerType, time.Now())

	if err != nil {
		logrus.WithError(err).Warn("failed to log alert trigger")
		return err
	}

	logrus.WithFields(logrus.Fields{
		"alert_id":     alertID,
		"symbol":       symbol,
		"trigger_type": triggerType,
		"chat_id":      chatID,
		"user_id":      userID,
		"username":     username,
	}).Debug("alert trigger logged")

	return nil
}

func (s *DatabaseStorage) LogPriceHistory(symbol string, price float64) error {
	_, err := s.db.Exec(`
		INSERT INTO price_history (symbol, price, timestamp)
		VALUES (?, ?, ?)`,
		symbol, price, time.Now())

	if err != nil {
		logrus.WithError(err).Warn("failed to log price history")
		return err
	}

	return nil
}

func (s *DatabaseStorage) GetTriggerHistory(chatID int64, limit int) []AlertTrigger {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT id, alert_id, symbol, trigger_price, chat_id, user_id, username, trigger_type, triggered_at
		FROM alert_triggers
		WHERE chat_id = ?
		ORDER BY triggered_at DESC
		LIMIT ?`,
		chatID, limit)

	if err != nil {
		logrus.WithError(err).Warn("failed to get alert trigger history")
		return nil
	}
	defer rows.Close()

	var triggers []AlertTrigger
	for rows.Next() {
		var trigger AlertTrigger
		err := rows.Scan(&trigger.ID, &trigger.AlertID, &trigger.Symbol, &trigger.TriggerPrice,
			&trigger.ChatID, &trigger.UserID, &trigger.Username, &trigger.TriggerType, &trigger.TriggeredAt)
		if err != nil {
			logrus.WithError(err).Warn("failed to scan alert trigger row")
			continue
		}
		triggers = append(triggers, trigger)
	}

	return triggers
}

// GetPreferredExchangeMarketForSymbol возвращает биржу и рынок для символа из первого найденного алерта или колла
func (s *DatabaseStorage) GetPreferredExchangeMarketForSymbol(symbol string) (string, string) {
	// Сначала проверяем алерты
	var exchange, market string
	err := s.db.QueryRow(`
		SELECT exchange, market FROM alerts 
		WHERE symbol = ? AND exchange != '' AND market != '' 
		LIMIT 1`, symbol).Scan(&exchange, &market)

	if err == nil && exchange != "" && market != "" {
		return exchange, market
	}

	// Если не нашли в алертах, проверяем открытые коллы
	err = s.db.QueryRow(`
		SELECT exchange, market FROM calls 
		WHERE symbol = ? AND status = 'open' AND exchange != '' AND market != '' 
		LIMIT 1`, symbol).Scan(&exchange, &market)

	if err == nil && exchange != "" && market != "" {
		return exchange, market
	}

	return "", ""
}
