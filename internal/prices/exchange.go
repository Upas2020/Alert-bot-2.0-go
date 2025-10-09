package prices

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"example.com/alert-bot/internal/config"
)

// ExchangeClients содержит HTTP клиенты для различных бирж.
type ExchangeClients struct {
	BitgetClient *http.Client
	BybitClient  *http.Client
}

// NewExchangeClients создает и инициализирует клиенты для бирж.
func NewExchangeClients(cfg config.Config) *ExchangeClients {
	bitgetClient := &http.Client{Timeout: 10 * time.Second}
	bybitClient := &http.Client{Timeout: 10 * time.Second}

	return &ExchangeClients{
		BitgetClient: bitgetClient,
		BybitClient:  bybitClient,
	}
}

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

// BybitTickerResponse описывает ответ Bybit API для тикеров
type BybitTickerResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		Category string        `json:"category"`
		List     []BybitTicker `json:"list"`
	} `json:"result"`
	Time int64 `json:"time"`
}

// BybitTicker структура одного тикера в ответе API
type BybitTicker struct {
	Symbol       string `json:"symbol"`
	LastPrice    string `json:"lastPrice"`
	Bid1Price    string `json:"bid1Price"`
	Ask1Price    string `json:"ask1Price"`
	PrevPrice24h string `json:"prevPrice24h"`
	Price24hPcnt string `json:"price24hPcnt"`
	HighPrice24h string `json:"highPrice24h"`
	LowPrice24h  string `json:"lowPrice24h"`
	MarkPrice    string `json:"markPrice,omitempty"` // Поле для фьючерсов
}

// BybitCandleResponse описывает ответ Bybit API для исторических данных (свечей)
type BybitCandleResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		Symbol   string     `json:"symbol"`
		Category string     `json:"category"`
		List     [][]string `json:"list"` // [timestamp, open, high, low, close, volume]
	} `json:"result"`
	Time int64 `json:"time"`
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

// FetchPriceInfoResult содержит информацию о цене и источнике
type FetchPriceInfoResult struct {
	PriceInfo
	Exchange string // "Bitget" или "Bybit"
	Market   string // "spot" или "futures"
}

// fetchWithURL общая функция для получения данных с Bitget
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
			if strings.Contains(source, "futures") && ticker.MarkPrice != "" && ticker.MarkPrice != "0" {
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

	available := make([]string, len(response.Data))
	for i, ticker := range response.Data {
		available[i] = ticker.Symbol
	}

	logrus.WithFields(logrus.Fields{
		"requested": symbol,
		"available": available[:min(10, len(available))], // Показываем только первые 10
		"source":    source,
	}).Warn("symbol not found in bitget response")

	return 0, fmt.Errorf("symbol %s not found in %s response", symbol, source)
}

// fetchHistoricalWithURL общая функция для получения исторических данных с Bitget
func fetchHistoricalWithURL(client *http.Client, url, symbol, source string) (float64, error) {
	logrus.WithFields(logrus.Fields{
		"url":    url,
		"source": source,
	}).Debug("bitget historical request")

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
		return 0, fmt.Errorf("bitget historical http status %d", resp.StatusCode)
	}

	var response BitgetCandleResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return 0, fmt.Errorf("failed to decode historical response: %w", err)
	}

	// Проверяем код ответа API
	if response.Code != "00000" {
		return 0, fmt.Errorf("bitget api error code=%s msg=%s", response.Code, response.Msg)
	}

	// Проверяем, что данные есть
	if len(response.Data) == 0 {
		return 0, fmt.Errorf("no historical data found for symbol %s on %s", symbol, source)
	}

	// Данные свечи: [timestamp, open, high, low, close, volume, quoteVolume, usdtVolume]
	// Берем цену закрытия последней свечи
	lastCandle := response.Data[len(response.Data)-1]
	if len(lastCandle) < 5 {
		return 0, fmt.Errorf("invalid bitget candle data format")
	}

	closePrice, err := parseFloat(lastCandle[4])
	if err != nil {
		return 0, fmt.Errorf("failed to parse close price from bitget candle: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"symbol":    symbol,
		"timestamp": lastCandle[0],
		"price":     closePrice,
		"source":    source,
	}).Debug("got historical price from Bitget")

	return closePrice, nil
}

// calculateChangePercent вычисляет процентное изменение
func calculateChangePercent(oldPrice, newPrice float64) float64 {
	if oldPrice == 0 {
		return 0
	}
	return ((newPrice - oldPrice) / oldPrice) * 100
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

// fetchBitgetSpotPriceOnly получает цену только со спота Bitget
func fetchBitgetSpotPriceOnly(client *http.Client, symbol string) (float64, error) {
	// Пробуем сначала API v2 для одного символа
	url := fmt.Sprintf("https://api.bitget.com/api/v2/spot/market/tickers?symbol=%s", symbol)
	price, err := fetchWithURL(client, url, symbol, "Bitget spot")
	if err == nil {
		return price, nil
	}

	logrus.WithError(err).WithField("symbol", symbol).Debug("failed to fetch with symbol param, trying without param")

	// Если не получилось, пробуем получить все тикеры и найти нужный
	url = "https://api.bitget.com/api/v2/spot/market/tickers"
	return fetchWithURL(client, url, symbol, "Bitget spot")
}

// fetchBitgetFuturesPrice получает цену с фьючерсного рынка Bitget
func fetchBitgetFuturesPrice(client *http.Client, symbol string) (float64, error) {
	// Пробуем получить конкретный символ на фьючерсах
	url := fmt.Sprintf("https://api.bitget.com/api/v2/mix/market/ticker?productType=USDT-FUTURES&symbol=%s", symbol)
	price, err := fetchWithURL(client, url, symbol, "Bitget futures")
	if err == nil {
		return price, nil
	}

	logrus.WithError(err).WithField("symbol", symbol).Debug("failed to fetch futures with symbol param, trying all tickers")

	// Если не получилось, получаем все фьючерсные тикеры
	url = "https://api.bitget.com/api/v2/mix/market/tickers?productType=USDT-FUTURES"
	return fetchWithURL(client, url, symbol, "Bitget futures")
}

// FetchBybitSpotPrice получает цену только со спота Bybit
func FetchBybitSpotPrice(client *http.Client, symbol string) (float64, error) {
	url := fmt.Sprintf("https://api.bybit.com/v5/market/tickers?category=spot&symbol=%s", symbol)
	price, err := fetchBybitWithURL(client, url, symbol, "Bybit spot")
	if err == nil {
		return price, nil
	}
	logrus.WithError(err).WithField("symbol", symbol).Debug("failed to fetch Bybit spot with symbol param, trying all tickers")

	url = "https://api.bybit.com/v5/market/tickers?category=spot"
	return fetchBybitWithURL(client, url, symbol, "Bybit spot")
}

// FetchBybitFuturesPrice получает цену с фьючерсного рынка Bybit
func FetchBybitFuturesPrice(client *http.Client, symbol string) (float64, error) {
	url := fmt.Sprintf("https://api.bybit.com/v5/market/tickers?category=linear&symbol=%s", symbol)
	price, err := fetchBybitWithURL(client, url, symbol, "Bybit futures")
	if err == nil {
		return price, nil
	}
	logrus.WithError(err).WithField("symbol", symbol).Debug("failed to fetch Bybit futures with symbol param, trying all tickers")

	url = "https://api.bybit.com/v5/market/tickers?category=linear"
	return fetchBybitWithURL(client, url, symbol, "Bybit futures")
}

func fetchBybitWithURL(client *http.Client, url, symbol, source string) (float64, error) {
	logrus.WithFields(logrus.Fields{
		"url":    url,
		"source": source,
	}).Debug("bybit request")

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
		return 0, fmt.Errorf("bybit http status %d", resp.StatusCode)
	}

	var response BybitTickerResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return 0, fmt.Errorf("failed to decode bybit response: %w", err)
	}

	if response.RetCode != 0 {
		return 0, fmt.Errorf("bybit api error code=%d msg=%s", response.RetCode, response.RetMsg)
	}

	if len(response.Result.List) == 0 {
		return 0, fmt.Errorf("no ticker data found for symbol %s on %s", symbol, source)
	}

	wanted := strings.ToUpper(symbol)
	for _, ticker := range response.Result.List {
		if strings.ToUpper(ticker.Symbol) == wanted {
			var priceStr string
			if strings.Contains(source, "futures") && ticker.MarkPrice != "" && ticker.MarkPrice != "0" {
				priceStr = ticker.MarkPrice
			} else {
				priceStr = ticker.LastPrice
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
			}).Debug("bybit parsed ticker successfully")

			return price, nil
		}
	}

	available := make([]string, min(10, len(response.Result.List)))
	for i, ticker := range response.Result.List {
		if i >= 10 {
			break
		}
		available[i] = ticker.Symbol
	}

	logrus.WithFields(logrus.Fields{
		"requested": symbol,
		"available": available,
		"source":    source,
	}).Warn("symbol not found in bybit response")

	return 0, fmt.Errorf("symbol %s not found in bybit %s response", symbol, source)
}

// fetchBybitHistoricalWithURL общая функция для получения исторических данных с Bybit
func fetchBybitHistoricalWithURL(client *http.Client, url, symbol, source string) (float64, error) {
	logrus.WithFields(logrus.Fields{
		"url":    url,
		"source": source,
	}).Debug("bybit historical request")

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
		return 0, fmt.Errorf("bybit historical http status %d", resp.StatusCode)
	}

	var response BybitCandleResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return 0, fmt.Errorf("failed to decode bybit historical response: %w", err)
	}

	if response.RetCode != 0 {
		return 0, fmt.Errorf("bybit api error code=%d msg=%s", response.RetCode, response.RetMsg)
	}

	if len(response.Result.List) == 0 {
		return 0, fmt.Errorf("no historical data found for symbol %s on %s", symbol, source)
	}

	lastCandle := response.Result.List[len(response.Result.List)-1]
	if len(lastCandle) < 5 {
		return 0, fmt.Errorf("invalid bybit candle data format")
	}

	closePrice, err := parseFloat(lastCandle[4])
	if err != nil {
		return 0, fmt.Errorf("failed to parse close price from bybit candle: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"symbol":    symbol,
		"timestamp": lastCandle[0],
		"price":     closePrice,
		"source":    source,
	}).Debug("got historical price from Bybit")

	return closePrice, nil
}

// fetchHistoricalPriceBitgetSpot получает историческую цену со спота Bitget
func fetchHistoricalPriceBitgetSpot(client *http.Client, symbol string, timestamp time.Time) (float64, error) {
	endTime := timestamp.UnixMilli()
	startTime := timestamp.Add(-2 * time.Minute).UnixMilli()

	url := fmt.Sprintf("https://api.bitget.com/api/v2/spot/market/candles?symbol=%s&granularity=1min&startTime=%d&endTime=%d&limit=5",
		symbol, startTime, endTime)

	logrus.WithFields(logrus.Fields{
		"url":       url,
		"symbol":    symbol,
		"timestamp": timestamp.Format(time.RFC3339),
		"source":    "Bitget spot",
	}).Debug("fetching historical price from Bitget spot")

	return fetchHistoricalWithURL(client, url, symbol, "Bitget spot")
}

// fetchHistoricalPriceBitgetFutures получает историческую цену с фьючерсов Bitget
func fetchHistoricalPriceBitgetFutures(client *http.Client, symbol string, timestamp time.Time) (float64, error) {
	endTime := timestamp.UnixMilli()
	startTime := timestamp.Add(-2 * time.Minute).UnixMilli()

	url := fmt.Sprintf("https://api.bitget.com/api/v2/mix/market/candles?symbol=%s&granularity=1m&startTime=%d&endTime=%d&limit=5&productType=USDT-FUTURES",
		symbol, startTime, endTime)

	logrus.WithFields(logrus.Fields{
		"url":       url,
		"symbol":    symbol,
		"timestamp": timestamp.Format(time.RFC3339),
		"source":    "Bitget futures",
	}).Debug("fetching historical price from Bitget futures")

	return fetchHistoricalWithURL(client, url, symbol, "Bitget futures")
}

// FetchBybitHistoricalPrice получает историческую цену с Bybit
func FetchBybitHistoricalPrice(client *http.Client, symbol string, timestamp time.Time, category string) (float64, error) {
	endTime := timestamp.UnixMilli()
	startTime := timestamp.Add(-2 * time.Minute).UnixMilli()

	url := fmt.Sprintf("https://api.bybit.com/v5/market/kline?category=%s&symbol=%s&interval=1&startTime=%d&endTime=%d&limit=5",
		category, symbol, startTime, endTime)

	logrus.WithFields(logrus.Fields{
		"url":       url,
		"symbol":    symbol,
		"timestamp": timestamp.Format(time.RFC3339),
		"source":    "Bybit " + category,
	}).Debug("fetching historical price from Bybit")

	return fetchBybitHistoricalWithURL(client, url, symbol, "Bybit "+category)
}

// FetchPriceInfo получает подробную информацию о цене с изменениями за разные периоды, проверяя биржи в порядке приоритета.
func FetchPriceInfo(clients *ExchangeClients, symbol string) (*FetchPriceInfoResult, error) {
	var currentPrice float64
	var sourceExchange, sourceMarket string
	var err error

	// 1. Bitget spot
	currentPrice, err = fetchBitgetSpotPriceOnly(clients.BitgetClient, symbol)
	if err == nil {
		sourceExchange = "Bitget"
		sourceMarket = "spot"
	} else {
		logrus.WithError(err).WithField("symbol", symbol).Debug("Bitget spot price fetch failed, trying Bitget futures")

		// 2. Bitget futures
		currentPrice, err = fetchBitgetFuturesPrice(clients.BitgetClient, symbol)
		if err == nil {
			sourceExchange = "Bitget"
			sourceMarket = "futures"
		} else {
			logrus.WithError(err).WithField("symbol", symbol).Debug("Bitget futures price fetch failed, trying Bybit spot")

			// 3. Bybit spot
			currentPrice, err = FetchBybitSpotPrice(clients.BybitClient, symbol)
			if err == nil {
				sourceExchange = "Bybit"
				sourceMarket = "spot"
			} else {
				logrus.WithError(err).WithField("symbol", symbol).Debug("Bybit spot price fetch failed, trying Bybit futures")

				// 4. Bybit futures
				currentPrice, err = FetchBybitFuturesPrice(clients.BybitClient, symbol)
				if err == nil {
					sourceExchange = "Bybit"
					sourceMarket = "futures"
				} else {
					return nil, fmt.Errorf("failed to get current price for %s from any source: %w", symbol, err)
				}
			}
		}
	}

	priceInfo := &PriceInfo{
		CurrentPrice: currentPrice,
		Source:       fmt.Sprintf("%s %s", sourceExchange, sourceMarket),
	}

	// Получаем исторические цены для разных периодов
	now := time.Now()

	// 15 минут назад
	if price15m, err := FetchHistoricalPrice(clients, symbol, now.Add(-15*time.Minute), sourceExchange, sourceMarket); err == nil {
		priceInfo.Change15m = calculateChangePercent(price15m, currentPrice)
	}

	// 1 час назад
	if price1h, err := FetchHistoricalPrice(clients, symbol, now.Add(-1*time.Hour), sourceExchange, sourceMarket); err == nil {
		priceInfo.Change1h = calculateChangePercent(price1h, currentPrice)
	}

	// 4 часа назад
	if price4h, err := FetchHistoricalPrice(clients, symbol, now.Add(-4*time.Hour), sourceExchange, sourceMarket); err == nil {
		priceInfo.Change4h = calculateChangePercent(price4h, currentPrice)
	}

	// 24 часа назад
	if price24h, err := FetchHistoricalPrice(clients, symbol, now.Add(-24*time.Hour), sourceExchange, sourceMarket); err == nil {
		priceInfo.Change24h = calculateChangePercent(price24h, currentPrice)
	}

	return &FetchPriceInfoResult{PriceInfo: *priceInfo, Exchange: sourceExchange, Market: sourceMarket}, nil
}

// FetchHistoricalPrice получает цену на определенный момент времени, проверяя биржи в порядке приоритета.
func FetchHistoricalPrice(clients *ExchangeClients, symbol string, timestamp time.Time, preferredExchange, preferredMarket string) (float64, error) {
	var price float64
	var err error

	// Если есть предпочтительная биржа/рынок, сначала пробуем их
	if preferredExchange == "Bitget" && preferredMarket == "spot" {
		price, err = fetchHistoricalPriceBitgetSpot(clients.BitgetClient, symbol, timestamp)
		if err == nil {
			return price, nil
		}
		logrus.WithError(err).WithField("symbol", symbol).Debug("Bitget spot historical price failed, trying preferred futures")
	}
	if preferredExchange == "Bitget" && preferredMarket == "futures" {
		price, err = fetchHistoricalPriceBitgetFutures(clients.BitgetClient, symbol, timestamp)
		if err == nil {
			return price, nil
		}
		logrus.WithError(err).WithField("symbol", symbol).Debug("Bitget futures historical price failed, trying Bybit spot")
	}
	if preferredExchange == "Bybit" && preferredMarket == "spot" {
		price, err = FetchBybitHistoricalPrice(clients.BybitClient, symbol, timestamp, "spot")
		if err == nil {
			return price, nil
		}
		logrus.WithError(err).WithField("symbol", symbol).Debug("Bybit spot historical price failed, trying Bybit futures")
	}
	if preferredExchange == "Bybit" && preferredMarket == "futures" {
		price, err = FetchBybitHistoricalPrice(clients.BybitClient, symbol, timestamp, "linear")
		if err == nil {
			return price, nil
		}
		logrus.WithError(err).WithField("symbol", symbol).Debug("Bybit futures historical price failed, trying Bitget spot")
	}

	// Если предпочтительный вариант не сработал или его не было, пробуем по порядку:

	// 1. Bitget spot
	price, err = fetchHistoricalPriceBitgetSpot(clients.BitgetClient, symbol, timestamp)
	if err == nil {
		return price, nil
	}
	logrus.WithError(err).WithField("symbol", symbol).Debug("Bitget spot historical price failed, trying Bitget futures")

	// 2. Bitget futures
	price, err = fetchHistoricalPriceBitgetFutures(clients.BitgetClient, symbol, timestamp)
	if err == nil {
		return price, nil
	}
	logrus.WithError(err).WithField("symbol", symbol).Debug("Bitget futures historical price failed, trying Bybit spot")

	// 3. Bybit spot
	price, err = FetchBybitHistoricalPrice(clients.BybitClient, symbol, timestamp, "spot")
	if err == nil {
		return price, nil
	}
	logrus.WithError(err).WithField("symbol", symbol).Debug("Bybit spot historical price failed, trying Bybit futures")

	// 4. Bybit futures
	price, err = FetchBybitHistoricalPrice(clients.BybitClient, symbol, timestamp, "linear")
	if err == nil {
		return price, nil
	}
	logrus.WithError(err).WithField("symbol", symbol).Debug("Bybit futures historical price failed")

	return 0, fmt.Errorf("failed to get historical price for %s from any source: %w", symbol, err)
}
