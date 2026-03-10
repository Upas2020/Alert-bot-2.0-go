package levels

import (
	"bytes"
	"fmt"
	"math"
	"strings"

	"github.com/wcharczuk/go-chart/v2"
	"github.com/wcharczuk/go-chart/v2/drawing"
)

type BasicChartGenerator struct {
	width  int
	height int
}

func NewBasicChartGenerator(width, height int) *BasicChartGenerator {
	return &BasicChartGenerator{
		width:  width,
		height: height,
	}
}

func (cg *BasicChartGenerator) GenerateChart(candles []Candle, levels []Level, symbol, timeframe string) ([]byte, error) {
	if len(candles) == 0 {
		return nil, fmt.Errorf("no candles data")
	}

	var yValues []float64
	var xValues []float64
	minPrice := math.Inf(1)
	maxPrice := math.Inf(-1)

	for i, candle := range candles {
		yValues = append(yValues, candle.Close)
		xValues = append(xValues, float64(i))
		minPrice = math.Min(minPrice, candle.Low)
		maxPrice = math.Max(maxPrice, candle.High)
	}

	series := chart.ContinuousSeries{
		Name:    symbol,
		XValues: xValues,
		YValues: yValues,
		Style: chart.Style{
			StrokeColor: drawing.ColorBlue,
			StrokeWidth: 2,
		},
	}

	// Текущая цена для заголовка
	currentPrice := candles[len(candles)-1].Close

	graph := chart.Chart{
		Title:  fmt.Sprintf("%s - %s Chart (Current: %.4f)", symbol, timeframe, currentPrice),
		Width:  cg.width,
		Height: cg.height,
		Background: chart.Style{
			Padding: chart.Box{
				Top:    20,
				Left:   20,
				Right:  60, // Увеличиваем отступ справа для цен
				Bottom: 20,
			},
		},
		XAxis: chart.XAxis{
			Name: "Time",
		},
		YAxis: chart.YAxis{
			Name: "Price",
			ValueFormatter: func(v interface{}) string {
				if vf, isFloat := v.(float64); isFloat {
					return fmt.Sprintf("%.6f", vf)
				}
				return ""
			},
			Range: &chart.ContinuousRange{
				Min: minPrice * 0.98,
				Max: maxPrice * 1.02,
			},
		},
		Series: []chart.Series{series},
	}

	for _, level := range levels {
		var levelColor drawing.Color
		switch level.Type {
		case "SUPPORT":
			levelColor = drawing.ColorGreen
		case "RESISTANCE":
			levelColor = drawing.ColorRed
		default:
			levelColor = drawing.ColorBlack
		}

		levelSeries := chart.ContinuousSeries{
			Name:    fmt.Sprintf("%.4f", level.Price),
			XValues: []float64{0, float64(len(candles) - 1)},
			YValues: []float64{level.Price, level.Price},
			Style: chart.Style{
				StrokeColor: levelColor,
				StrokeWidth: 2,
				DotWidth:    0,
			},
		}

		graph.Series = append(graph.Series, levelSeries)
	}

	// Убираем легенду для чистого вида
	graph.Elements = []chart.Renderable{}

	buffer := bytes.NewBuffer([]byte{})
	err := graph.Render(chart.PNG, buffer)
	if err != nil {
		return nil, fmt.Errorf("failed to render chart: %w", err)
	}

	return buffer.Bytes(), nil
}

func (cg *BasicChartGenerator) GenerateTextChart(candles []Candle, levels []Level, symbol, timeframe string) string {
	if len(candles) == 0 {
		return "No candle data available"
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("📊 *%s - %s Analysis*\n\n", symbol, timeframe))

	currentPrice := candles[len(candles)-1].Close
	result.WriteString(fmt.Sprintf("💰 Current Price: *%.6f*\n\n", currentPrice))

	if len(levels) > 0 {
		result.WriteString("🎯 *Support & Resistance Levels:*\n")

		supportLevels := []Level{}
		resistanceLevels := []Level{}

		for _, level := range levels {
			if level.Type == "SUPPORT" {
				supportLevels = append(supportLevels, level)
			} else if level.Type == "RESISTANCE" {
				resistanceLevels = append(resistanceLevels, level)
			}
		}

		if len(supportLevels) > 0 {
			result.WriteString("\n🟢 *Support Levels:*\n")
			for _, level := range supportLevels {
				distance := ((currentPrice - level.Price) / currentPrice) * 100
				result.WriteString(fmt.Sprintf("  • `%.6f` - %.2f%% below current\n",
					level.Price, distance))
			}
		}

		if len(resistanceLevels) > 0 {
			result.WriteString("\n🔴 *Resistance Levels:*\n")
			for _, level := range resistanceLevels {
				distance := ((level.Price - currentPrice) / currentPrice) * 100
				result.WriteString(fmt.Sprintf("  • `%.6f` - %.2f%% above current\n",
					level.Price, distance))
			}
		}
	}

	result.WriteString(fmt.Sprintf("\n📈 Analysis based on %d candles\n", len(candles)))
	result.WriteString("📅 Data source: Bitget API")

	return result.String()
}
