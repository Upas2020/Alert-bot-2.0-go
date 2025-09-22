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
	// Поля для фьючерсов
	MarkPrice   string `json:"markPrice,omitempty"`
	IndexPrice  string `json:"indexPrice,omitempty"`
	FundingRate string `json:"fundingRate,omitempty"`
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
	Source       string  // "spot" или "futures"
}

// FetchSpotPrice получает последнюю цену для символа с fallback на фьючерсы
func FetchSpotPrice(client *http.Client, symbol string) (float64, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	// Сначала пробуем спот
	price, err := fetchSpotPriceOnly(client, symbol)
	if err == nil {
		return price, nil
	}

	logrus.WithError(err).WithField("symbol", symbol).Debug("spot price fetch failed, trying futures")

	// Если спот не работает, пробуем фьючерсы
	return fetchFuturesPrice(client, symbol)
}

// fetchSpotPriceOnly получает цену только со спота
func fetchSpotPriceOnly(client *http.Client, symbol string) (float64, error) {
	// Пробуем сначала API v2 для одного символа
	url := fmt.Sprintf("https://api.bitget.com/api/v2/spot/market/tickers?symbol=%s", symbol)
	price, err := fetchWithURL(client, url, symbol, "spot")
	if err == nil {
		return price, nil
	}

	logrus.WithError(err).Debug("failed to fetch with symbol param, trying without param")

	// Если не получилось, пробуем получить все тикеры и найти нужный
	url = "https://api.bitget.com/api/v2/spot/market/tickers"
	return fetchWithURL(client, url, symbol, "spot")
}

// fetchFuturesPrice получает цену с фьючерсного рынка
func fetchFuturesPrice(client *http.Client, symbol string) (float64, error) {
	// Пробуем получить конкретный символ на фьючерсах
	url := fmt.Sprintf("https://api.bitget.com/api/v2/mix/market/ticker?productType=USDT-FUTURES&symbol=%s", symbol)
	price, err := fetchWithURL(client, url, symbol, "futures")
	if err == nil {
		return price, nil
	}

	logrus.WithError(err).Debug("failed to fetch futures with symbol param, trying all tickers")

	// Если не получилось, получаем все фьючерсные тикеры
	url = "https://api.bitget.com/api/v2/mix/market/tickers?productType=USDT-FUTURES"
	return fetchWithURL(client, url, symbol, "futures")
}

// FetchPriceInfo получает подробную информацию о цене с изменениями за разные периоды
func FetchPriceInfo(client *http.Client, symbol string) (*PriceInfo, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	// Получаем текущую цену (сначала со спота, потом с фьючерсов)
	currentPrice, err := FetchSpotPrice(client, symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to get current price: %w", err)
	}

	priceInfo := &PriceInfo{
		CurrentPrice: currentPrice,
		Source:       "spot", // По умолчанию считаем что со спота, если нужно можем улучшить логику
	}

	// Получаем исторические цены для разных периодов
	now := time.Now()

	// 15 минут назад
	if price15m, err := FetchHistoricalPrice(client, symbol, now.Add(-15*time.Minute)); err == nil {
		priceInfo.Change15m = calculateChangePercent(price15m, currentPrice)
	}

	// 1 час назад
	if price1h, err := FetchHistoricalPrice(client, symbol, now.Add(-1*time.Hour)); err == nil {
		priceInfo.Change1h = calculateChangePercent(price1h, currentPrice)
	}

	// 4 часа назад
	if price4h, err := FetchHistoricalPrice(client, symbol, now.Add(-4*time.Hour)); err == nil {
		priceInfo.Change4h = calculateChangePercent(price4h, currentPrice)
	}

	// 24 часа назад
	if price24h, err := FetchHistoricalPrice(client, symbol, now.Add(-24*time.Hour)); err == nil {
		priceInfo.Change24h = calculateChangePercent(price24h, currentPrice)
	}

	return priceInfo, nil
}

// FetchHistoricalPrice получает цену на определенный момент времени с fallback на фьючерсы
func FetchHistoricalPrice(client *http.Client, symbol string, timestamp time.Time) (float64, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	// Сначала пробуем спот
	price, err := fetchHistoricalPriceSpot(client, symbol, timestamp)
	if err == nil {
		return price, nil
	}

	logrus.WithError(err).WithField("symbol", symbol).Debug("spot historical price failed, trying futures")

	// Если спот не работает, пробуем фьючерсы
	return fetchHistoricalPriceFutures(client, symbol, timestamp)
}

// fetchHistoricalPriceSpot получает историческую цену со спота
func fetchHistoricalPriceSpot(client *http.Client, symbol string, timestamp time.Time) (float64, error) {
	endTime := timestamp.UnixMilli()
	startTime := timestamp.Add(-2 * time.Minute).UnixMilli()

	url := fmt.Sprintf("https://api.bitget.com/api/v2/spot/market/candles?symbol=%s&granularity=1min&startTime=%d&endTime=%d&limit=5",
		symbol, startTime, endTime)

	logrus.WithFields(logrus.Fields{
		"url":       url,
		"symbol":    symbol,
		"timestamp": timestamp.Format(time.RFC3339),
		"source":    "spot",
	}).Debug("fetching historical price")

	return fetchHistoricalWithURL(client, url, symbol, "spot")
}

// fetchHistoricalPriceFutures получает историческую цену с фьючерсов
func fetchHistoricalPriceFutures(client *http.Client, symbol string, timestamp time.Time) (float64, error) {
	endTime := timestamp.UnixMilli()
	startTime := timestamp.Add(-2 * time.Minute).UnixMilli()

	url := fmt.Sprintf("https://api.bitget.com/api/v2/mix/market/candles?symbol=%s&granularity=1m&startTime=%d&endTime=%d&limit=5&productType=USDT-FUTURES",
		symbol, startTime, endTime)

	logrus.WithFields(logrus.Fields{
		"url":       url,
		"symbol":    symbol,
		"timestamp": timestamp.Format(time.RFC3339),
		"source":    "futures",
	}).Debug("fetching historical price from futures")

	return fetchHistoricalWithURL(client, url, symbol, "futures")
}

// fetchHistoricalWithURL общая функция для получения исторических данных
func fetchHistoricalWithURL(client *http.Client, url, symbol, source string) (float64, error) {
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
		return 0, fmt.Errorf("no candle data found for %s from %s", symbol, source)
	}

	// Берем последнюю доступную свечу
	lastCandle := response.Data[len(response.Data)-1]
	if len(lastCandle) < 5 {
		return 0, fmt.Errorf("invalid candle data format")
	}

	// Индекс 4 это close price
	closePrice, err := parseFloat(lastCandle[4])
	if err != nil {
		return 0, fmt.Errorf("failed to parse close price from candle: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"symbol":    symbol,
		"timestamp": lastCandle[0],
		"price":     closePrice,
		"source":    source,
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

func fetchWithURL(client *http.Client, url, symbol, source string) (float64, error) {
	logrus.WithFields(logrus.Fields{
		"url":    url,
		"source": source,
	}).Debug("bitget request")

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
		return 0, fmt.Errorf("no ticker data found for symbol %s on %s", symbol, source)
	}

	// Ищем точное совпадение символа
	wanted := strings.ToUpper(symbol)
	for _, ticker := range response.Data {
		if strings.ToUpper(ticker.Symbol) == wanted {
			// Для фьючерсов приоритет markPrice, если есть
			var priceStr string
			if source == "futures" && ticker.MarkPrice != "" && ticker.MarkPrice != "0" {
				priceStr = ticker.MarkPrice
			} else {
				priceStr = ticker.LastPr
			}

			price, err := parseFloat(priceStr)
			if err != nil {
				return 0, fmt.Errorf("failed to parse price '%s': %w", priceStr, err)
			}

			logrus.WithFields(logrus.Fields{
				"symbol":    ticker.Symbol,
				"price":     price,
				"requested": symbol,
				"source":    source,
				"change24h": ticker.Change24h,
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
		"available": available[:min(10, len(available))], // Показываем только первые 10
		"source":    source,
	}).Warn("symbol not found in response")

	return 0, fmt.Errorf("symbol %s not found in %s response", symbol, source)
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

// min helper function
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
