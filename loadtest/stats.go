//go:build loadtest

package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// result is one completed (or failed) request.
type result struct {
	endpoint string
	status   int // 0 when the request never got a response (transport error)
	latency  time.Duration
	err      error
}

// bucket classifies a status code the way the report groups it. 429 is
// broken out on its own: it is the signal T2-D's rate-limit defaults are
// meant to produce, once that story lands on this branch.
func bucket(status int) string {
	switch {
	case status == 0:
		return "transport-error"
	case status == 429:
		return "429"
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return "other"
	}
}

// percentile uses the nearest-rank method over a slice already sorted
// ascending: index = ceil(p/100 * n) - 1, clamped to the valid range. This is
// exact, not an approximation — p99 is not "the max", it is the value at the
// 99th-percentile rank, and for small n those coincide (correctly) rather
// than by construction.
func percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

type summary struct {
	count    int
	buckets  map[string]int
	p50      time.Duration
	p95      time.Duration
	p99      time.Duration
	max      time.Duration
	min      time.Duration
	throughp float64 // req/s, only meaningful for the overall summary
}

func summarize(rs []result, wall time.Duration) summary {
	s := summary{buckets: map[string]int{}}
	lat := make([]time.Duration, 0, len(rs))
	for _, r := range rs {
		s.count++
		s.buckets[bucket(r.status)]++
		lat = append(lat, r.latency)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	s.p50 = percentile(lat, 50)
	s.p95 = percentile(lat, 95)
	s.p99 = percentile(lat, 99)
	if len(lat) > 0 {
		s.min = lat[0]
		s.max = lat[len(lat)-1]
	}
	if wall > 0 {
		s.throughp = float64(s.count) / wall.Seconds()
	}
	return s
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
}

// report prints the overall and per-endpoint summaries. Kept as plain text —
// this is read by a human closing out T2-F, not consumed by another program.
func report(rs []result, wall time.Duration) {
	overall := summarize(rs, wall)

	fmt.Println()
	fmt.Println("==================== load test report ====================")
	fmt.Printf("wall clock:      %s\n", wall.Round(time.Millisecond))
	fmt.Printf("total requests:  %d\n", overall.count)
	fmt.Printf("throughput:      %.1f req/s\n", overall.throughp)
	fmt.Printf("latency:         p50=%s p95=%s p99=%s max=%s min=%s\n",
		ms(overall.p50), ms(overall.p95), ms(overall.p99), ms(overall.max), ms(overall.min))
	fmt.Printf("status codes:    2xx=%d 4xx=%d 429=%d 5xx=%d transport-error=%d\n",
		overall.buckets["2xx"], overall.buckets["4xx"], overall.buckets["429"],
		overall.buckets["5xx"], overall.buckets["transport-error"])

	byEndpoint := map[string][]result{}
	var order []string
	for _, r := range rs {
		if _, ok := byEndpoint[r.endpoint]; !ok {
			order = append(order, r.endpoint)
		}
		byEndpoint[r.endpoint] = append(byEndpoint[r.endpoint], r)
	}
	sort.Strings(order)

	fmt.Println()
	fmt.Println("---- per-endpoint ----")
	for _, name := range order {
		es := summarize(byEndpoint[name], wall)
		fmt.Printf("%-48s n=%-6d p50=%-8s p95=%-8s p99=%-8s max=%-8s 2xx=%d 4xx=%d 429=%d 5xx=%d err=%d\n",
			name, es.count, ms(es.p50), ms(es.p95), ms(es.p99), ms(es.max),
			es.buckets["2xx"], es.buckets["4xx"], es.buckets["429"], es.buckets["5xx"], es.buckets["transport-error"])
	}
	fmt.Println("============================================================")
}
