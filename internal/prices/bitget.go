package prices

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// BitgetTickerResponse описывает ответ Bitget API v2 для тикеров
type BitgetTickerResponse struct {
	Code        string         `json:"code"`
	Msg         string         `json:"msg"`
	RequestTime int64          `json:"requestTime"`
	Data        []BitgetTicker `json:"data"`
}

// BitgetTicker структура одного тикера в ответе API v2 (актуальная)
type BitgetTicker struct {
	Symbol       string `json:"symbol"`
	Open         string `json:"open"`
	High24h      string `json:"high24h"`
	Low24h       string `json:"low24h"`
	LastPr       string `json:"lastPr"` // Текущая цена
	QuoteVolume  string `json:"quoteVolume"`
	BaseVolume   string `json:"baseVolume"`
	UsdtVolume   string `json:"usdtVolume"`
	Ts           string `json:"ts"`
	BidPr        string `json:"bidPr"` // Цена покупки
	AskPr        string `json:"askPr"` // Цена продажи
	BidSz        string `json:"bidSz"`
	AskSz        string `json:"askSz"`
	OpenUtc      string `json:"openUtc"`
	ChangeUtc24h string `json:"changeUtc24h"`
	Change24h    string `json:"change24h"`
}

// BitgetCandleResponse описывает ответ для исторических данных (свечей)
type BitgetCandleResponse struct {
	Code        string     `json:"code"`
	Msg         string     `json:"msg"`
	RequestTime int64      `json:"requestTime"`
	Data        [][]string `json:"data"` // Массив массивов строк [timestamp, open, high, low, close, volume, quoteVolume, usdtVolume]
}

// PriceInfo содержит информацию о цене и изменениях
type PriceInfo struct {
	CurrentPrice float64
	Change15m    float64 // Процентное изменение за 15 минут
	Change1h     float64 // Процентное изменение за 1 час
	Change4h     float64 // Процентное изменение за 4 часа
	Change24h    float64 // Процентное изменение за 24 часа
}

// FetchSpotPrice получает последнюю цену для символа Spot через Bitget API v2
func FetchSpotPrice(client *http.Client, symbol string) (float64, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	// Пробуем сначала API v2 для одного символа
	url := fmt.Sprintf("https://api.bitget.com/api/v2/spot/market/tickers?symbol=%s", symbol)
	price, err := fetchWithURL(client, url, symbol)
	if err == nil {
		return price, nil
	}

	logrus.WithError(err).Debug("failed to fetch with symbol param, trying without param")

	// Если не получилось, пробуем получить все тикеры и найти нужный
	url = "https://api.bitget.com/api/v2/spot/market/tickers"
	return fetchWithURL(client, url, symbol)
}

// FetchPriceInfo получает подробную информацию о цене с изменениями за разные периоды
func FetchPriceInfo(client *http.Client, symbol string) (*PriceInfo, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	// Получаем текущую цену
	currentPrice, err := FetchSpotPrice(client, symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get current price: %w", err)
	}

	priceInfo := &PriceInfo{
		CurrentPrice: currentPrice,
	}

	// Получаем исторические цены для разных периодов
	now := time.Now()

	// 15 минут назад
	if price15m, err := fetchHistoricalPrice(client, symbol, now.Add(-15*time.Minute)); err == nil {
		priceInfo.Change15m = calculateChangePercent(price15m, currentPrice)
	}

	// 1 час назад
	if price1h, err := fetchHistoricalPrice(client, symbol, now.Add(-1*time.Hour)); err == nil {
		priceInfo.Change1h = calculateChangePercent(price1h, currentPrice)
	}

	// 4 часа назад
	if price4h, err := fetchHistoricalPrice(client, symbol, now.Add(-4*time.Hour)); err == nil {
		priceInfo.Change4h = calculateChangePercent(price4h, currentPrice)
	}

	// 24 часа назад
	if price24h, err := fetchHistoricalPrice(client, symbol, now.Add(-24*time.Hour)); err == nil {
		priceInfo.Change24h = calculateChangePercent(price24h, currentPrice)
	}

	return priceInfo, nil
}

// fetchHistoricalPrice получает цену на определенный момент времени
func fetchHistoricalPrice(client *http.Client, symbol string, timestamp time.Time) (float64, error) {
	// Используем 1-минутные свечи и берем одну свечу ближайшую к нужному времени
	endTime := timestamp.UnixMilli()
	startTime := timestamp.Add(-2 * time.Minute).UnixMilli() // Небольшой буфер

	url := fmt.Sprintf("https://api.bitget.com/api/v2/spot/market/candles?symbol=%s&granularity=1min&startTime=%d&endTime=%d&limit=5",
		symbol, startTime, endTime)

	logrus.WithFields(logrus.Fields{
		"url":       url,
		"symbol":    symbol,
		"timestamp": timestamp.Format(time.RFC3339),
	}).Debug("fetching historical price")

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("bitget http status %d", resp.StatusCode)
	}

	var response BitgetCandleResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return 0, fmt.Errorf("failed to decode candle response: %w", err)
	}

	if response.Code != "00000" {
		return 0, fmt.Errorf("bitget api error code=%s msg=%s", response.Code, response.Msg)
	}

	if len(response.Data) == 0 {
		return 0, fmt.Errorf("no candle data found for %s at %s", symbol, timestamp.Format(time.RFC3339))
	}

	// Берем последнюю доступную свечу (самую близкую к нужному времени)
	lastCandle := response.Data[len(response.Data)-1]
	if len(lastCandle) < 5 {
		return 0, fmt.Errorf("invalid candle data format")
	}

	// Индекс 4 это close price (цена закрытия)
	closePrice, err := parseFloat(lastCandle[4])
	if err != nil {
		return 0, fmt.Errorf("failed to parse close price from candle: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"symbol":    symbol,
		"timestamp": lastCandle[0],
		"price":     closePrice,
	}).Debug("got historical price")

	return closePrice, nil
}

// calculateChangePercent вычисляет процентное изменение
func calculateChangePercent(oldPrice, newPrice float64) float64 {
	if oldPrice == 0 {
		return 0
	}
	return ((newPrice - oldPrice) / oldPrice) * 100
}

func fetchWithURL(client *http.Client, url, symbol string) (float64, error) {
	logrus.WithField("url", url).Debug("bitget request")

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("bitget http status %d", resp.StatusCode)
	}

	var response BitgetTickerResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	// Проверяем код ответа API
	if response.Code != "00000" {
		return 0, fmt.Errorf("bitget api error code=%s msg=%s", response.Code, response.Msg)
	}

	// Проверяем, что данные есть
	if len(response.Data) == 0 {
		return 0, fmt.Errorf("no ticker data found for symbol %s", symbol)
	}

	// Ищем точное совпадение символа
	wanted := strings.ToUpper(symbol)
	for _, ticker := range response.Data {
		if strings.ToUpper(ticker.Symbol) == wanted {
			price, err := parseFloat(ticker.LastPr) // Используем lastPr вместо close
			if err != nil {
				return 0, fmt.Errorf("failed to parse lastPr price '%s': %w", ticker.LastPr, err)
			}

			logrus.WithFields(logrus.Fields{
				"symbol":    ticker.Symbol,
				"price":     price,
				"requested": symbol,
				"change24h": ticker.Change24h,
				"open":      ticker.Open,
			}).Debug("bitget parsed ticker successfully")

			return price, nil
		}
	}

	// Если точное совпадение не найдено, показываем доступные символы
	available := make([]string, len(response.Data))
	for i, ticker := range response.Data {
		available[i] = ticker.Symbol
	}

	logrus.WithFields(logrus.Fields{
		"requested": symbol,
		"available": available,
	}).Warn("symbol not found in response")

	return 0, fmt.Errorf("symbol %s not found in response", symbol)
}

// parseFloat более надежная версия парсинга float из строки
func parseFloat(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty price string")
	}

	// Используем strconv.ParseFloat для более точного парсинга
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float format: %s", s)
	}

	return f, nil
}

// FormatPrice форматирует цену, убирая лишние нули
func FormatPrice(price float64) string {
	// Используем strconv.FormatFloat с 'g' для автоматического убирания лишних нулей
	formatted := strconv.FormatFloat(price, 'g', -1, 64)

	// Проверяем, не получилась ли экспоненциальная запись для маленьких чисел
	if strings.Contains(formatted, "e") && price > 0.000001 {
		// Для чисел больше 0.000001 используем фиксированный формат
		formatted = strconv.FormatFloat(price, 'f', -1, 64)
		formatted = strings.TrimRight(formatted, "0")
		formatted = strings.TrimRight(formatted, ".")
	}

	return formatted
}

// FormatPriceWithSymbol форматирует цену с символом для удобства
func FormatPriceWithSymbol(symbol string, price float64) string {
	return fmt.Sprintf("%s: %s", symbol, FormatPrice(price))
}
