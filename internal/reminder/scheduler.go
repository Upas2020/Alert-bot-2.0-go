package reminder

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Scheduler struct {
	db  *sql.DB
	api *tgbotapi.BotAPI

	mu    sync.Mutex
	tasks map[string]*time.Timer
}

func NewScheduler(db *sql.DB, api *tgbotapi.BotAPI) *Scheduler {
	return &Scheduler{db: db, api: api, tasks: make(map[string]*time.Timer)}
}

func (s *Scheduler) Start(ctx context.Context) {
	// загружаем будущие таски
	tasks, _ := GetPendingReminders(s.db)
	for _, t := range tasks {
		s.schedule(ctx, t)
	}

	// фоновый сборщик просроченных
	tick := time.NewTicker(1 * time.Minute)
	go func() {
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				DeleteExpiredReminders(s.db)
			}
		}
	}()
}

func (s *Scheduler) schedule(ctx context.Context, t Task) {
	dur := time.Until(t.Trigger)
	if dur <= 0 {
		s.fire(t)
		return
	}
	s.mu.Lock()
	s.tasks[t.ID] = time.AfterFunc(dur, func() { s.fire(t) })
	s.mu.Unlock()
}

func (s *Scheduler) fire(t Task) {
	msg := fmt.Sprintf("🔔 Посмотри на график %s", t.Symbol)
	if t.Text != "" {
		msg += fmt.Sprintf(", %s", t.Text)
	}
	s.api.Send(tgbotapi.NewMessage(t.ChatID, msg))
	DeleteReminder(s.db, t.ID)
	s.mu.Lock()
	delete(s.tasks, t.ID)
	s.mu.Unlock()
}

// Add создаёт таск и ставит на таймер
func (s *Scheduler) Add(ctx context.Context, chatID, userID int64, username, symbol, text string, dur time.Duration) (string, error) {
	id := genID()
	task := Task{
		ID:       id,
		ChatID:   chatID,
		UserID:   userID,
		Username: username,
		Symbol:   symbol,
		Text:     text,
		Trigger:  time.Now().Add(dur),
	}
	if err := InsertReminder(s.db, task); err != nil {
		return "", err
	}
	s.schedule(ctx, task)
	return id, nil
}

func genID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
