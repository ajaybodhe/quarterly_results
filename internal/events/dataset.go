package events

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"quarterly_results/internal/source/nasdaq"
)

// WriteUniverse saves a stock universe as CSV (symbol, name, market_cap_b,
// sector, industry) so a dataset build is reproducible.
func WriteUniverse(path string, stocks []nasdaq.ScreenerStock) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"symbol", "name", "market_cap_b", "sector", "industry"})
	for _, s := range stocks {
		w.Write([]string{s.Symbol, s.Name, strconv.FormatFloat(s.MarketCapB, 'f', 2, 64), s.Sector, s.Industry})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReadUniverse returns the symbol column of a universe CSV.
func ReadUniverse(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	var out []string
	for i, r := range rows {
		if i == 0 || len(r) == 0 || strings.TrimSpace(r[0]) == "" {
			continue // header / blank
		}
		out = append(out, strings.TrimSpace(r[0]))
	}
	return out, nil
}

// DatasetStats summarises a BuildDataset run.
type DatasetStats struct {
	Symbols    int // universe size
	Resumed    int // already recorded in the .done file
	Built      int // processed this run with ≥1 event
	Empty      int // processed this run with no events (e.g. no Item 2.02 history)
	Failed     int
	Duplicates int // another share class of a company already built
	Events     int // events written this run
}

// BuildDataset builds events for every universe symbol not yet recorded in
// out+".done", appending to out as each symbol completes, so an interrupted
// build can simply be re-run. Share classes of one company (same CIK) are
// built once.
func BuildDataset(b *Builder, symbols []string, out string, workers int, now time.Time,
	progress func(format string, a ...any)) (DatasetStats, error) {
	stats := DatasetStats{Symbols: len(symbols)}
	donePath := out + ".done"
	doneSyms, doneCIKs, err := readDone(donePath)
	if err != nil {
		return stats, err
	}
	doneFile, err := os.OpenFile(donePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return stats, err
	}
	defer doneFile.Close()

	var mu sync.Mutex // guards stats, doneCIKs, the output and .done files
	record := func(sym string, cik int, status string, n int, msg string) {
		fmt.Fprintf(doneFile, "%s\t%s\t%d\t%d\t%s\n", sym, status, cik, n, strings.ReplaceAll(msg, "\n", " "))
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	processed := 0
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sym := range jobs {
				cik, _ := b.sec.CIK(sym)
				mu.Lock()
				if cik != 0 && doneCIKs[cik] {
					stats.Duplicates++
					record(sym, cik, "duplicate", 0, "")
					mu.Unlock()
					continue
				}
				if cik != 0 {
					doneCIKs[cik] = true // reserve before building so share classes don't race
				}
				mu.Unlock()

				evs, err := b.Build(sym, now)

				mu.Lock()
				processed++
				switch {
				case err != nil:
					stats.Failed++
					if cik != 0 {
						delete(doneCIKs, cik) // let another share class (or a re-run) try this company
					}
					record(sym, cik, "failed", 0, err.Error())
					progress("[%d/%d] %s: failed: %v", processed, len(symbols)-stats.Resumed, sym, err)
				default:
					if werr := Append(out, evs); werr != nil {
						mu.Unlock()
						progress("Fatal: writing %s: %v", out, werr)
						continue
					}
					stats.Events += len(evs)
					if len(evs) == 0 {
						stats.Empty++
					} else {
						stats.Built++
					}
					record(sym, cik, "ok", len(evs), "")
					progress("[%d/%d] %s: %d events", processed, len(symbols)-stats.Resumed, sym, len(evs))
				}
				mu.Unlock()
			}
		}()
	}
	for _, sym := range symbols {
		if doneSyms[strings.ToUpper(sym)] {
			stats.Resumed++
			continue
		}
		jobs <- sym
	}
	close(jobs)
	wg.Wait()
	return stats, nil
}

// readDone loads the symbols and CIKs already processed by earlier runs.
// Failed symbols are not marked done, so a re-run retries them.
func readDone(path string) (map[string]bool, map[int]bool, error) {
	syms, ciks := map[string]bool{}, map[int]bool{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return syms, ciks, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) < 3 || parts[1] == "failed" {
			continue
		}
		syms[strings.ToUpper(parts[0])] = true
		if cik, err := strconv.Atoi(parts[2]); err == nil && cik != 0 && parts[1] == "ok" {
			ciks[cik] = true
		}
	}
	return syms, ciks, sc.Err()
}
