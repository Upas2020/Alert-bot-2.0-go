package alerts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type Alert struct {
	ID            string    `json:"id"`
	ChatID        int64     `json:"chat_id"`
	Symbol        string    `json:"symbol"`
	TargetPrice   float64   `json:"target_price,omitempty"`
	TargetPercent float64   `json:"target_percent,omitempty"`
	BasePrice     float64   `json:"base_price,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Storage struct {
	mu   sync.RWMutex
	byID map[string]Alert
	path string
}

func generateShortID() string {
	bytes := make([]byte, 4) // 4 байта = 8 hex символов
	if _, err := rand.Read(bytes); err != nil {
		// fallback на UUID если crypto/rand не работает
		return uuid.NewString()[:8]
	}
	return hex.EncodeToString(bytes)
}

func NewStorage(dir string) (*Storage, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	st := &Storage{byID: make(map[string]Alert), path: filepath.Join(dir, "alerts.json")}
	_ = st.load()
	return st, nil
}

func (s *Storage) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logrus.Debug("alerts file not found, starting with empty storage")
			return nil
		}
		return err
	}
	defer f.Close()
	var arr []Alert
	if err := json.NewDecoder(f).Decode(&arr); err != nil {
		return err
	}
	for _, a := range arr {
		s.byID[a.ID] = a
	}
	logrus.WithField("count", len(arr)).Debug("loaded alerts from storage")
	return nil
}

func (s *Storage) persist() error {
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	arr := make([]Alert, 0, len(s.byID))
	for _, a := range s.byID {
		arr = append(arr, a)
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].CreatedAt.Before(arr[j].CreatedAt) })
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(arr); err != nil {
		f.Close()
		return err
	}
	f.Close() // Закрываем файл перед rename

	// Retry rename с небольшой задержкой
	for i := 0; i < 3; i++ {
		if err := os.Rename(tmp, s.path); err == nil {
			return nil
		}
		// Если файл заблокирован, пробуем удалить старый
		if i == 0 {
			os.Remove(s.path)
		}
		if i < 2 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return os.Rename(tmp, s.path)
}

func (s *Storage) Add(alert Alert) (Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if alert.ID == "" {
		// Генерируем уникальный короткий ID
		for {
			alert.ID = generateShortID()
			if _, exists := s.byID[alert.ID]; !exists {
				break
			}
		}
	}
	if alert.CreatedAt.IsZero() {
		alert.CreatedAt = time.Now()
	}
	s.byID[alert.ID] = alert
	return alert, s.persist()
}

func (s *Storage) Update(alert Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if alert.ID == "" {
		return errors.New("alert id is empty")
	}
	s.byID[alert.ID] = alert
	return s.persist()
}

func (s *Storage) ListByChat(chatID int64) []Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Alert
	for _, a := range s.byID {
		if a.ChatID == chatID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Storage) DeleteByID(chatID int64, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.byID[id]; ok && a.ChatID == chatID {
		delete(s.byID, id)
		return true, s.persist()
	}
	return false, nil
}

func (s *Storage) DeleteAllByChat(chatID int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for id, a := range s.byID {
		if a.ChatID == chatID {
			delete(s.byID, id)
			count++
		}
	}
	return count, s.persist()
}

func (s *Storage) GetBySymbol(symbol string) []Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Alert
	for _, a := range s.byID {
		if a.Symbol == symbol {
			out = append(out, a)
		}
	}
	return out
}

func (s *Storage) GetAllSymbols() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := map[string]struct{}{}
	for _, a := range s.byID {
		if a.Symbol != "" {
			set[a.Symbol] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for sym := range set {
		out = append(out, sym)
	}
	sort.Strings(out)
	logrus.WithField("symbols", out).Debug("get all symbols from alerts")
	return out
}
