package reminder

import (
	"database/sql"
)

func InsertReminder(db *sql.DB, t Task) error {
	_, err := db.Exec(`
		INSERT INTO reminders(id,chat_id,user_id,username,symbol,text,trigger_at)
		VALUES(?,?,?,?,?,?,?)`,
		t.ID, t.ChatID, t.UserID, t.Username, t.Symbol, t.Text, t.Trigger)
	return err
}

func DeleteReminder(db *sql.DB, id string) {
	db.Exec("DELETE FROM reminders WHERE id = ?", id)
}

func DeleteExpiredReminders(db *sql.DB) {
	db.Exec("DELETE FROM reminders WHERE trigger_at < datetime('now')")
}

func GetPendingReminders(db *sql.DB) ([]Task, error) {
	rows, err := db.Query(`
		SELECT id,chat_id,user_id,username,symbol,text,trigger_at
		FROM reminders
		WHERE trigger_at > datetime('now')
		ORDER BY trigger_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		var t Task
		if err = rows.Scan(&t.ID, &t.ChatID, &t.UserID, &t.Username, &t.Symbol, &t.Text, &t.Trigger); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}
