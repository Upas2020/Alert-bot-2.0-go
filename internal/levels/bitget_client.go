package levels

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type BitgetClient struct {
	baseURL    string
	httpClient *http.Client
}

type BitgetCandleResponse struct {
	Code        string     `json:"code"`
	Msg         string     `json:"msg"`
	RequestTime int64      `json:"requestTime"`
	Data        [][]string `json:"data"`
}

func NewBitgetClient(baseURL string) *BitgetClient {
	return &BitgetClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *BitgetClient) GetCandles(symbol, granularity string, limit int) ([]Candle, error) {
	endpoint := c.baseURL + "/api/v2/spot/market/history-candles"

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("granularity", granularity)
	capped := limit
	if capped <= 0 {
		capped = 100
	} else if capped > 200 {
		capped = 200
	}
	params.Add("limit", strconv.Itoa(capped))
	params.Add("endTime", strconv.FormatInt(time.Now().UnixMilli(), 10))

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	resp, err := c.httpClient.Get(fullURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get candles: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var candleResp BitgetCandleResponse
	if err := json.Unmarshal(body, &candleResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if candleResp.Code != "00000" {
		return nil, fmt.Errorf("API error: %s", candleResp.Msg)
	}

	candles := make([]Candle, 0, len(candleResp.Data))
	for _, data := range candleResp.Data {
		if len(data) < 7 {
			continue
		}

		timestamp, _ := strconv.ParseInt(data[0], 10, 64)
		open, _ := strconv.ParseFloat(data[1], 64)
		high, _ := strconv.ParseFloat(data[2], 64)
		low, _ := strconv.ParseFloat(data[3], 64)
		close, _ := strconv.ParseFloat(data[4], 64)
		volume, _ := strconv.ParseFloat(data[5], 64)

		candles = append(candles, Candle{
			Timestamp: timestamp,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     close,
			Volume:    volume,
		})
	}

	return candles, nil
}

func ParseTimeframe(tf string) (string, error) {
	tf = strings.ToUpper(tf)
	switch tf {
	case "1m", "1M":
		return "1min", nil
	case "5m", "5M":
		return "5min", nil
	case "15m", "15M":
		return "15min", nil
	case "30m", "30M":
		return "30min", nil
	case "1h", "1H":
		return "1h", nil
	case "4h", "4H":
		return "4h", nil
	case "6h", "6H":
		return "6h", nil
	case "12h", "12H":
		return "12h", nil
	case "1d", "1D":
		return "1day", nil
	case "1w", "1W":
		return "1week", nil
	default:
		return "1day", fmt.Errorf("unsupported timeframe: %s, using default 1D", tf)
	}
}
