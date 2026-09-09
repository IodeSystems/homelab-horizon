package monitor

import (
	"sort"
	"time"
)

// Bucketed history.
//
// The raw form — every sample of every check — is quadratic to render and
// grows with the fleet: N checks × 100 samples on the wire, and a chart whose
// x-axis is the union of all their timestamps, so every check gets a cell in
// every other check's columns too. Two vantages over a handful of domains was
// enough to put six figures of DOM nodes on the page.
//
// So the server answers the question the page actually asks: over this
// window, in this many columns, what did each check look like. Time is
// bucketed to a fixed column count, and consecutive buckets holding the same
// status collapse into one run. A check that was up the whole time is one
// run, whatever the fleet size or the sample count.

// defaultBuckets is the column count when the caller does not say. Roughly a
// pixel every few units at readable widths, and fine enough that a single
// failed interval still gets its own column in a normal window.
const defaultBuckets = 120

// maxBuckets caps what a caller can ask for. Past this the response stops
// being cheaper than the raw samples it replaced.
const maxBuckets = 480

// minBucket is the floor on a bucket's width. Below a second, buckets are
// finer than the data.
const minBucket = time.Second

// HistoryRun is a stretch of consecutive buckets that all held one status.
//
// Field names are short because they repeat once per run per series, and the
// whole point of this shape is what it costs on the wire.
type HistoryRun struct {
	Status string `json:"s"`
	From   int    `json:"f"`
	Len    int    `json:"n"`
}

// HistorySeries is one check over the window.
type HistorySeries struct {
	Name    string `json:"name"`
	Type    string `json:"type,omitempty"`
	Target  string `json:"target,omitempty"`
	Vantage string `json:"vantage,omitempty"`

	// Runs covers the whole window in order, so the ribbon is drawn from
	// these alone with no per-bucket iteration.
	Runs []HistoryRun `json:"runs"`

	// Latency is one value per bucket, -1 where no sample landed. The worst
	// (slowest) sample in the bucket, not the mean: at this resolution a mean
	// hides exactly the spike worth seeing.
	Latency []int32 `json:"lat"`

	Samples int    `json:"samples"`
	Worst   string `json:"worst"`
	Steady  bool   `json:"steady"`
}

// BucketedHistory is the whole response.
type BucketedHistory struct {
	From     int64           `json:"from"`     // unix ms at the start of bucket 0
	BucketMs int64           `json:"bucketMs"` // width of one bucket
	Buckets  int             `json:"buckets"`
	Series   []HistorySeries `json:"series"`
}

// statusRank orders statuses by how much they need looking at. Bucketing
// takes the worst status in a bucket rather than the last, because an outage
// that fits inside one column must not be averaged out of existence.
func statusRank(s string) int {
	switch s {
	case StatusFailed:
		return 3
	case StatusWarning:
		return 2
	case StatusOK:
		return 1
	default:
		return 0
	}
}

func worseOf(a, b string) string {
	if statusRank(b) > statusRank(a) {
		return b
	}
	return a
}

// BucketedHistory renders every check's history into a fixed number of
// columns. buckets <= 0 means the default.
func (m *Monitor) BucketedHistory(buckets int) BucketedHistory {
	if buckets <= 0 {
		buckets = defaultBuckets
	}
	if buckets > maxBuckets {
		buckets = maxBuckets
	}

	all := m.GetAllHistory()
	statuses := map[string]CheckStatus{}
	for _, c := range m.GetStatuses() {
		statuses[c.Name] = c
	}

	// The window spans every sample hz holds, so all series share one axis.
	var first, last time.Time
	for _, results := range all {
		for _, r := range results {
			if r.Timestamp.IsZero() {
				continue
			}
			if first.IsZero() || r.Timestamp.Before(first) {
				first = r.Timestamp
			}
			if r.Timestamp.After(last) {
				last = r.Timestamp
			}
		}
	}
	if first.IsZero() {
		return BucketedHistory{Buckets: 0, Series: []HistorySeries{}}
	}

	span := last.Sub(first) + time.Millisecond
	bucketDur := span / time.Duration(buckets)
	if bucketDur < minBucket {
		bucketDur = minBucket
	}

	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)

	out := BucketedHistory{
		From:     first.UnixMilli(),
		BucketMs: bucketDur.Milliseconds(),
		Buckets:  buckets,
		Series:   make([]HistorySeries, 0, len(names)),
	}

	for _, name := range names {
		results := all[name]
		if len(results) == 0 {
			continue
		}

		cells := make([]string, buckets)
		lat := make([]int32, buckets)
		for i := range lat {
			lat[i] = -1
		}

		for _, r := range results {
			if r.Timestamp.IsZero() {
				continue
			}
			idx := int(r.Timestamp.Sub(first) / bucketDur)
			if idx < 0 {
				idx = 0
			}
			if idx >= buckets {
				idx = buckets - 1
			}
			cells[idx] = worseOf(cells[idx], r.Status)
			if v := int32(r.Latency); v > lat[idx] {
				lat[idx] = v
			}
		}

		// Forward-fill the status so the ribbon is continuous between a
		// check's intervals. Latency is deliberately not filled: a repeated
		// number would be a measurement nobody took.
		carry := ""
		for i := range cells {
			if cells[i] == "" {
				cells[i] = carry
			} else {
				carry = cells[i]
			}
		}

		series := HistorySeries{
			Name:    name,
			Samples: len(results),
			Latency: lat,
			Runs:    encodeRuns(cells),
		}
		if st, ok := statuses[name]; ok {
			series.Type = st.Type
			series.Target = st.Target
			series.Vantage = st.Vantage
		}
		for _, run := range series.Runs {
			series.Worst = worseOf(series.Worst, run.Status)
		}
		// Steady means one status for the whole window — including the
		// leading gap before a check's first sample, which is its own run and
		// is why this is not simply len(runs) == 1.
		series.Steady = len(series.Runs) == 1 ||
			(len(series.Runs) == 2 && series.Runs[0].Status == "")

		out.Series = append(out.Series, series)
	}

	return out
}

// encodeRuns collapses consecutive equal statuses into runs.
func encodeRuns(cells []string) []HistoryRun {
	if len(cells) == 0 {
		return []HistoryRun{}
	}
	runs := make([]HistoryRun, 0, 8)
	start := 0
	for i := 1; i <= len(cells); i++ {
		if i == len(cells) || cells[i] != cells[start] {
			runs = append(runs, HistoryRun{Status: cells[start], From: start, Len: i - start})
			start = i
		}
	}
	return runs
}
