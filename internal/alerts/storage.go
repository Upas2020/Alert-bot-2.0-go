package alerts

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	_ "modernc.org/sqlite" // Возвращаем pure Go SQLite драйвер
)

type Alert struct {
	ID            string    `json:"id"`
	ChatID        int64     `json:"chat_id"`
	UserID        int64     `json:"user_id"`  // ID пользователя Telegram
	Username      string    `json:"username"` // Username пользователя Telegram
	Symbol        string    `json:"symbol"`
	Market        string    `json:"market"` // "spot" или "futures"
	TargetPrice   float64   `json:"target_price,omitempty"`
	TargetPercent float64   `json:"target_percent,omitempty"`
	BasePrice     float64   `json:"base_price,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Call struct {
	ID         string     `json:"id"`
	UserID     int64      `json:"user_id"`
	Username   string     `json:"username"`
	ChatID     int64      `json:"chat_id"`
	Symbol     string     `json:"symbol"`
	Market     string     `json:"market"`    // "spot" или "futures"
	Direction  string     `json:"direction"` // "long" или "short"
	EntryPrice float64    `json:"entry_price"`
	Size       float64    `json:"size"` // Размер позиции (от 0 до 100)
	ExitPrice  float64    `json:"exit_price,omitempty"`
	PnlPercent float64    `json:"pnl_percent,omitempty"`
	Status     string     `json:"status"` // "open" или "closed"
	OpenedAt   time.Time  `json:"opened_at"`
	ClosedAt   *time.Time `json:"closed_at,omitempty"`
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
	UserID       int64   `json:"user_id"`
	Username     string  `json:"username"`
	TotalCalls   int     `json:"total_calls"`
	ClosedCalls  int     `json:"closed_calls"`
	WinningCalls int     `json:"winning_calls"`
	TotalPnl     float64 `json:"total_pnl"`
	AveragePnl   float64 `json:"average_pnl"`
	WinRate      float64 `json:"win_rate"`
	BestCall     float64 `json:"best_call"`
	WorstCall    float64 `json:"worst_call"`
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

func (s *DatabaseStorage) migrate() error {
	queries := []string{
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
			market TEXT DEFAULT ''
		)`,
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
			market TEXT DEFAULT ''
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
		`ALTER TABLE alert_triggers ADD COLUMN user_id INTEGER DEFAULT 0`,
		`ALTER TABLE alert_triggers ADD COLUMN username TEXT DEFAULT ''`,
		`ALTER TABLE calls ADD COLUMN market TEXT DEFAULT ''`,
		`ALTER TABLE calls ADD COLUMN size REAL DEFAULT 100`,
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
		INSERT INTO alerts (id, chat_id, user_id, username, symbol, market, target_price, target_percent, base_price, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		alert.ID, alert.ChatID, alert.UserID, alert.Username, alert.Symbol, alert.Market,
		alert.TargetPrice, alert.TargetPercent, alert.BasePrice, alert.CreatedAt)

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
		SET chat_id = ?, user_id = ?, username = ?, symbol = ?, market = ?, target_price = ?, target_percent = ?, base_price = ?
		WHERE id = ?`,
		alert.ChatID, alert.UserID, alert.Username, alert.Symbol, alert.Market,
		alert.TargetPrice, alert.TargetPercent, alert.BasePrice, alert.ID)

	return err
}

func (s *DatabaseStorage) ListByChat(chatID int64) []Alert {
	rows, err := s.db.Query(`
		SELECT id, chat_id, COALESCE(user_id, 0), COALESCE(username, ''), symbol, market, target_price, target_percent, base_price, created_at
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
			&alert.TargetPrice, &alert.TargetPercent, &alert.BasePrice, &alert.CreatedAt)
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
		SELECT id, chat_id, COALESCE(user_id, 0), COALESCE(username, ''), symbol, market, target_price, target_percent, base_price, created_at
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
			&alert.TargetPrice, &alert.TargetPercent, &alert.BasePrice, &alert.CreatedAt)
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
		INSERT INTO calls (id, user_id, username, chat_id, symbol, market, direction, entry_price, size, status, opened_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		call.ID, call.UserID, call.Username, call.ChatID, call.Symbol, call.Market,
		call.Direction, call.EntryPrice, call.Size, call.Status, call.OpenedAt)

	if err != nil {
		return call, err
	}

	logrus.WithFields(logrus.Fields{
		"call_id":     call.ID,
		"user_id":     call.UserID,
		"username":    call.Username,
		"symbol":      call.Symbol,
		"direction":   call.Direction,
		"entry_price": call.EntryPrice,
	}).Info("call opened")

	return call, nil
}

func (s *DatabaseStorage) CloseCall(callID string, userID int64, exitPrice float64, sizeToClose float64) error {
	// Получаем информацию о колле
	var call Call
	err := s.db.QueryRow(`
		SELECT id, user_id, username, chat_id, symbol, market, direction, entry_price, size, status
		FROM calls WHERE id = ? AND user_id = ? AND status = 'open'`,
		callID, userID).Scan(
		&call.ID, &call.UserID, &call.Username, &call.ChatID,
		&call.Symbol, &call.Market, &call.Direction, &call.EntryPrice, &call.Size, &call.Status)

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

	// Вычисляем PnL в зависимости от направления для закрываемой части
	// PnL для закрытой части рассчитывается как обычно
	var pnlPercentForClosedPart float64
	if call.Direction == "long" {
		pnlPercentForClosedPart = ((exitPrice - call.EntryPrice) / call.EntryPrice) * 100
	} else { // short
		pnlPercentForClosedPart = ((call.EntryPrice - exitPrice) / call.EntryPrice) * 100
	}

	newSize := call.Size - sizeToClose
	status := "open"
	var closedAt sql.NullTime

	// Если оставшийся размер очень мал, считаем колл полностью закрытым
	if newSize < 0.001 {
		status = "closed"
		now := time.Now()
		closedAt = sql.NullTime{Time: now, Valid: true}
		newSize = 0.0 // Устанавливаем в 0 для полного закрытия
	}

	// Обновляем колл в базе данных
	// exit_price и pnl_percent будут относиться к последней операции закрытия
	// size будет оставшимся размером
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

func (s *DatabaseStorage) GetUserCalls(userID int64, onlyOpen bool) []Call {
	query := `
		SELECT id, user_id, username, chat_id, symbol, market, direction, entry_price, size, 
		       COALESCE(exit_price, 0), COALESCE(pnl_percent, 0), status, opened_at, closed_at
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

func (s *DatabaseStorage) GetAllOpenCalls() []Call {
	rows, err := s.db.Query(`
		SELECT id, user_id, username, chat_id, symbol, market, direction, entry_price, size, 
		       COALESCE(exit_price, 0), COALESCE(pnl_percent, 0), status, opened_at, closed_at
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
		WHERE user_id = ? AND opened_at >= datetime('now', '-90 days')
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
		WHERE opened_at >= datetime('now', '-90 days')
		GROUP BY user_id, username
		HAVING closed_calls > 0
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
		WHERE user_id = ? AND opened_at >= datetime('now', '-90 days')
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
		       COALESCE(exit_price, 0), COALESCE(pnl_percent, 0), status, opened_at, closed_at
		FROM calls 
		WHERE id = ? AND user_id = ?`,
		callID, userID).Scan(
		&call.ID, &call.UserID, &call.Username, &call.ChatID,
		&call.Symbol, &call.Market, &call.Direction, &call.EntryPrice, &call.Size, &call.ExitPrice,
		&call.PnlPercent, &call.Status, &call.OpenedAt, &closedAt)

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
		SELECT id, alert_id, symbol, trigger_price, chat_id, COALESCE(user_id, 0), COALESCE(username, ''), trigger_type, triggered_at
		FROM alert_triggers 
		WHERE chat_id = ?
		ORDER BY triggered_at DESC
		LIMIT ?`, chatID, limit)

	if err != nil {
		logrus.WithError(err).Warn("failed to get trigger history")
		return nil
	}
	defer rows.Close()

	var triggers []AlertTrigger
	for rows.Next() {
		var trigger AlertTrigger
		err := rows.Scan(&trigger.ID, &trigger.AlertID, &trigger.Symbol,
			&trigger.TriggerPrice, &trigger.ChatID, &trigger.UserID, &trigger.Username, &trigger.TriggerType, &trigger.TriggeredAt)
		if err != nil {
			logrus.WithError(err).Warn("failed to scan trigger row")
			continue
		}
		triggers = append(triggers, trigger)
	}

	return triggers
}

func (s *DatabaseStorage) GetSymbolStats() map[string]int {
	rows, err := s.db.Query(`
		SELECT symbol, COUNT(*) as count
		FROM alerts 
		WHERE symbol != ''
		GROUP BY symbol
		ORDER BY count DESC`)

	if err != nil {
		logrus.WithError(err).Warn("failed to get symbol stats")
		return nil
	}
	defer rows.Close()

	stats := make(map[string]int)
	for rows.Next() {
		var symbol string
		var count int
		if err := rows.Scan(&symbol, &count); err != nil {
			logrus.WithError(err).Warn("failed to scan symbol stats")
			continue
		}
		stats[symbol] = count
	}

	return stats
}

func (s *DatabaseStorage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
