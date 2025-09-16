package prices

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// SymbolProvider интерфейс для получения актуального списка символов
type SymbolProvider interface {
	GetAllSymbols() []string
}

// PriceMonitor периодически опрашивает цены и сообщает об изменениях через callback.
type PriceMonitor struct {
	Client           *http.Client
	SymbolProvider   SymbolProvider // Провайдер для получения актуального списка символов
	ThresholdPercent float64
	Interval         time.Duration

	mu          sync.Mutex
	lastPriceBy map[string]float64
}

// NewPriceMonitor конструктор.
func NewPriceMonitor(symbols []string, thresholdPercent float64, intervalSec int) *PriceMonitor {
	if intervalSec <= 0 {
		intervalSec = 30
	}
	return &PriceMonitor{
		Client:           &http.Client{Timeout: 10 * time.Second},
		ThresholdPercent: thresholdPercent,
		Interval:         time.Duration(intervalSec) * time.Second,
		lastPriceBy:      make(map[string]float64),
	}
}

// NewPriceMonitorWithProvider создает монитор с провайдером символов, запрашивает цены каждые 60 секунд
func NewPriceMonitorWithProvider(provider SymbolProvider, thresholdPercent float64, intervalSec int) *PriceMonitor {
	if intervalSec <= 0 {
		intervalSec = 60
	}
	return &PriceMonitor{
		Client:           &http.Client{Timeout: 10 * time.Second},
		SymbolProvider:   provider,
		ThresholdPercent: thresholdPercent,
		Interval:         time.Duration(intervalSec) * time.Second,
		lastPriceBy:      make(map[string]float64),
	}
}

// Run запускает мониторинг до завершения контекста. На значимое изменение вызывает onAlert(symbol, old, new, deltaPercent).
func (m *PriceMonitor) Run(ctx context.Context, onAlert func(string, float64, float64, float64)) error {
	ticker := time.NewTicker(m.Interval)
	defer ticker.Stop()

	// Первый проход сразу
	m.poll(onAlert)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.poll(onAlert)
		}
	}
}

func (m *PriceMonitor) poll(onAlert func(string, float64, float64, float64)) {
	// Получаем актуальный список символов
	var symbols []string
	if m.SymbolProvider != nil {
		symbols = m.SymbolProvider.GetAllSymbols()
		if len(symbols) == 0 {
			logrus.Debug("no symbols to monitor from provider")
			return
		}
	}

	// Очищаем устаревшие цены (символы, которые больше не отслеживаются)
	if m.SymbolProvider != nil {
		m.cleanupOldPrices(symbols)
	}

	for _, sym := range symbols {
		price, err := FetchSpotPrice(m.Client, sym)
		if err != nil {
			logrus.WithError(err).WithField("symbol", sym).Warn("fetch price failed")
			continue
		}

		m.mu.Lock()
		prev, had := m.lastPriceBy[sym]
		m.lastPriceBy[sym] = price
		m.mu.Unlock()

		if !had || prev == 0 {
			logrus.WithFields(logrus.Fields{
				"symbol": sym,
				"price":  price,
			}).Debug("initial price recorded")
			continue
		}

		delta := price - prev
		deltaPct := (delta / prev) * 100.0
		if deltaPct >= m.ThresholdPercent || deltaPct <= -m.ThresholdPercent {
			onAlert(sym, prev, price, deltaPct)
		}
	}
}

// cleanupOldPrices удаляет цены для символов, которые больше не отслеживаются
func (m *PriceMonitor) cleanupOldPrices(currentSymbols []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Создаем map для быстрого поиска текущих символов
	symbolSet := make(map[string]struct{})
	for _, sym := range currentSymbols {
		symbolSet[sym] = struct{}{}
	}

	// Удаляем символы, которых нет в текущем списке
	for sym := range m.lastPriceBy {
		if _, exists := symbolSet[sym]; !exists {
			delete(m.lastPriceBy, sym)
			logrus.WithField("symbol", sym).Debug("removed unused symbol from price cache")
		}
	}
}

// GetCachedPrice возвращает последнюю кешированную цену для символа
func (m *PriceMonitor) GetCachedPrice(symbol string) (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	price, exists := m.lastPriceBy[symbol]
	return price, exists
}
