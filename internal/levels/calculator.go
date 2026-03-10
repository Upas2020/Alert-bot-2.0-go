package levels

import (
	"math"
	"sort"
)

type Candle struct {
	Timestamp int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

type Calculator struct {
	lookbackPeriod int
	minTouches     int
	rangePercent   float64
}

type Level struct {
	Price    float64
	Touches  int
	Type     string
	Strength string
}

func NewCalculator(lookbackPeriod, minTouches int, rangePercent float64) *Calculator {
	return &Calculator{
		lookbackPeriod: lookbackPeriod,
		minTouches:     minTouches,
		rangePercent:   rangePercent,
	}
}

func (c *Calculator) CalculateLevels(candles []Candle, currentPrice float64) []Level {
	if len(candles) == 0 {
		return nil
	}

	bodyTops := make([]float64, len(candles))
	bodyBottoms := make([]float64, len(candles))

	for i, candle := range candles {
		bodyTops[i] = math.Max(candle.Close, candle.Open)
		bodyBottoms[i] = math.Min(candle.Close, candle.Open)
	}

	topRanges := c.findRangeLevels(bodyTops, true)
	bottomRanges := c.findRangeLevels(bodyBottoms, false)

	var allLevels []Level

	for _, levelPrice := range topRanges {
		touches := c.countLevelTouches(levelPrice, candles)
		if touches >= c.minTouches {
			allLevels = append(allLevels, Level{
				Price:    levelPrice,
				Touches:  touches,
				Type:     c.determineLevelType(levelPrice, currentPrice),
				Strength: c.determineStrength(touches),
			})
		}
	}

	for _, levelPrice := range bottomRanges {
		touches := c.countLevelTouches(levelPrice, candles)
		if touches >= c.minTouches {
			if !c.isDuplicate(allLevels, levelPrice) {
				allLevels = append(allLevels, Level{
					Price:    levelPrice,
					Touches:  touches,
					Type:     c.determineLevelType(levelPrice, currentPrice),
					Strength: c.determineStrength(touches),
				})
			} else {
				c.updateIfMoreTouches(allLevels, levelPrice, touches, currentPrice)
			}
		}
	}

	extremes := c.findExtremes(candles, currentPrice)
	for _, extreme := range extremes {
		if !c.isDuplicate(allLevels, extreme.Price) {
			allLevels = append(allLevels, extreme)
		}
	}

	return allLevels
}

func (c *Calculator) findRangeLevels(prices []float64, isTop bool) []float64 {
	if len(prices) == 0 {
		return nil
	}

	sorted := make([]float64, len(prices))
	copy(sorted, prices)

	if isTop {
		sort.Sort(sort.Reverse(sort.Float64Slice(sorted)))
	} else {
		sort.Float64s(sorted)
	}

	var ranges []float64

	for _, price := range sorted {
		inExistingRange := false

		for _, rangeLevel := range ranges {
			rangeMin := rangeLevel * (1 - c.rangePercent/100)
			rangeMax := rangeLevel * (1 + c.rangePercent/100)

			if price >= rangeMin && price <= rangeMax {
				inExistingRange = true
				break
			}
		}

		if !inExistingRange {
			ranges = append(ranges, price)
		}
	}

	return ranges
}

func (c *Calculator) countLevelTouches(level float64, candles []Candle) int {
	rangeMin := level * (1 - c.rangePercent/100)
	rangeMax := level * (1 + c.rangePercent/100)

	touches := 0
	for _, candle := range candles {
		if candle.Low <= rangeMax && candle.High >= rangeMin {
			touches++
		}
	}

	return touches
}

func (c *Calculator) determineLevelType(levelPrice, currentPrice float64) string {
	if currentPrice > levelPrice {
		return "SUPPORT"
	}
	return "RESISTANCE"
}

func (c *Calculator) determineStrength(touches int) string {
	if touches >= 15 {
		return "HIGH"
	} else if touches >= 10 {
		return "MEDIUM"
	} else if touches >= 7 {
		return "MEDIUM"
	}
	return "LOW"
}

func (c *Calculator) isDuplicate(levels []Level, price float64) bool {
	threshold := c.rangePercent / 2

	for _, level := range levels {
		diff := math.Abs(price-level.Price) / level.Price * 100
		if diff < threshold {
			return true
		}
	}

	return false
}

func (c *Calculator) updateIfMoreTouches(levels []Level, price float64, touches int, currentPrice float64) {
	threshold := c.rangePercent / 2

	for i := range levels {
		diff := math.Abs(price-levels[i].Price) / levels[i].Price * 100
		if diff < threshold && touches > levels[i].Touches {
			levels[i].Price = price
			levels[i].Touches = touches
			levels[i].Type = c.determineLevelType(price, currentPrice)
			levels[i].Strength = c.determineStrength(touches)
			break
		}
	}
}

func (c *Calculator) findExtremes(candles []Candle, currentPrice float64) []Level {
	if len(candles) == 0 {
		return nil
	}

	var highestBodyTop, lowestBodyBottom float64
	highestBodyTop = 0
	lowestBodyBottom = math.MaxFloat64

	for _, candle := range candles {
		bodyTop := math.Max(candle.Close, candle.Open)
		bodyBottom := math.Min(candle.Close, candle.Open)

		if bodyTop > highestBodyTop {
			highestBodyTop = bodyTop
		}
		if bodyBottom < lowestBodyBottom {
			lowestBodyBottom = bodyBottom
		}
	}

	extremes := []Level{
		{
			Price:    highestBodyTop,
			Touches:  9999,
			Type:     c.determineLevelType(highestBodyTop, currentPrice),
			Strength: "EXTREME",
		},
		{
			Price:    lowestBodyBottom,
			Touches:  9999,
			Type:     c.determineLevelType(lowestBodyBottom, currentPrice),
			Strength: "EXTREME",
		},
	}

	return extremes
}
