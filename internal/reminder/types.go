package reminder

import "time"

type Task struct {
	ID       string
	ChatID   int64
	UserID   int64
	Username string
	Symbol   string
	Text     string
	Trigger  time.Time
}
