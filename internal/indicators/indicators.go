package indicators

import (
	"math"
	"sort"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
)

// PctChange returns (newVal - oldVal) / |oldVal| × 100.
// Returns 0 when oldVal is 0 to avoid division by zero.
func PctChange(oldVal, newVal float64) float64 {
	if oldVal == 0 {
		return 0
	}
	return (newVal - oldVal) / math.Abs(oldVal) * 100
}

// ClosestPrice returns the most recent closing price at or before target.
// The points slice must be sorted oldest → newest.
func ClosestPrice(points []domain.PricePoint, target time.Time) (float64, bool) {
	var result float64
	found := false
	for _, p := range points {
		if p.Date.After(target) {
			break
		}
		result = p.Close
		found = true
	}
	return result, found
}

// OpenOnDate returns the opening price for the exact calendar date of target.
// Returns 0 if no entry matches (e.g. data unavailable or market closed).
func OpenOnDate(points []domain.PricePoint, target time.Time) float64 {
	targetDate := target.Format("2006-01-02")
	for _, p := range points {
		if p.Date.Format("2006-01-02") == targetDate {
			return p.Open
		}
	}
	return 0
}

func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]float64, len(xs))
	copy(cp, xs)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 0 {
		return (cp[mid-1] + cp[mid]) / 2
	}
	return cp[mid]
}

// DailyVolatility returns the standard deviation of daily percentage returns
// over the last n calendar days of prices (oldest→newest). Returns 0 if
// insufficient data.
func DailyVolatility(prices []domain.PricePoint, days int) float64 {
	if len(prices) < 2 {
		return 0
	}
	cutoff := prices[len(prices)-1].Date.AddDate(0, 0, -days)
	var rets []float64
	for i := 1; i < len(prices); i++ {
		if prices[i].Date.Before(cutoff) {
			continue
		}
		if prices[i-1].Close == 0 {
			continue
		}
		rets = append(rets, PctChange(prices[i-1].Close, prices[i].Close))
	}
	if len(rets) < 2 {
		return 0
	}
	var sum float64
	for _, r := range rets {
		sum += r
	}
	mean := sum / float64(len(rets))
	var sq float64
	for _, r := range rets {
		d := r - mean
		sq += d * d
	}
	return math.Sqrt(sq / float64(len(rets)-1))
}

// RSI14 computes the 14-period Wilder RSI from a sorted (oldest→newest)
// price slice. Returns 50 if there is insufficient data.
func RSI14(prices []domain.PricePoint) float64 {
	const period = 14
	if len(prices) < period+1 {
		return 50
	}
	// Use only the most recent period+1 prices for the seed average,
	// then apply Wilder's smoothing over any remaining bars.
	tail := prices[len(prices)-min(len(prices), period*3):]

	var avgGain, avgLoss float64
	for i := 1; i <= period && i < len(tail); i++ {
		delta := tail[i].Close - tail[i-1].Close
		if delta > 0 {
			avgGain += delta
		} else {
			avgLoss -= delta
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	// Wilder smoothing for remaining bars.
	for i := period + 1; i < len(tail); i++ {
		delta := tail[i].Close - tail[i-1].Close
		gain, loss := 0.0, 0.0
		if delta > 0 {
			gain = delta
		} else {
			loss = -delta
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
	}

	if avgLoss == 0 {
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs)
}

// ReactionDay returns the correct reaction day and prior-close price given:
//   - announceDate: the date the company released earnings (8-K or calendar date)
//   - earningsTime: "bmo" (before market open) or "amc"/"" (after close or unknown)
//   - prices: sorted price history (oldest → newest)
//
// BMO: earnings known before market opens → market reacts that same day.
//
//	priorClose = close on (announceDate − 1), reactionDay = announceDate.
//
// AMC / unknown: earnings known after close → market reacts next working day.
//
//	priorClose = close on announceDate, reactionDay = exchange.NextWorkingDay(announceDate).
//
// Falls back to price-gap heuristic when earningsTime is "" and both days have data.
func ReactionDay(announceDate time.Time, earningsTime string, prices []domain.PricePoint) (
	reactionDay time.Time, priorClose float64, ok bool,
) {
	dayBeforeClose, okDayBefore := ClosestPrice(prices, announceDate.AddDate(0, 0, -1))
	announceDayClose, okAnnounce := ClosestPrice(prices, announceDate)

	switch earningsTime {
	case "bmo":
		if !okDayBefore || dayBeforeClose == 0 {
			return time.Time{}, 0, false
		}
		return announceDate, dayBeforeClose, true

	case "amc":
		if !okAnnounce || announceDayClose == 0 {
			return time.Time{}, 0, false
		}
		return exchange.NextWorkingDay(announceDate), announceDayClose, true

	default:
		// Heuristic: compare absolute gap at announce-day open vs next-day open.
		// Whichever side has the larger gap is likely the reaction day.
		announceDayOpen := OpenOnDate(prices, announceDate)
		nextDay := exchange.NextWorkingDay(announceDate)
		nextDayOpen := OpenOnDate(prices, nextDay)

		if okDayBefore && dayBeforeClose > 0 && announceDayOpen > 0 &&
			okAnnounce && announceDayClose > 0 && nextDayOpen > 0 {
			gapAnnounce := math.Abs(announceDayOpen-dayBeforeClose) / dayBeforeClose
			gapNextDay := math.Abs(nextDayOpen-announceDayClose) / announceDayClose
			if gapAnnounce > gapNextDay {
				return announceDate, dayBeforeClose, okDayBefore
			}
			return nextDay, announceDayClose, okAnnounce
		}
		// Not enough price data — prefer AMC assumption (safer default).
		if okAnnounce && announceDayClose > 0 {
			return exchange.NextWorkingDay(announceDate), announceDayClose, true
		}
		return time.Time{}, 0, false
	}
}
