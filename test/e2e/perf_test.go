//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Performance scenarios run against an optimized (non-race) build and write a
// report with latency, throughput, memory and CPU/alloc hotspots, plus a list
// of optimization opportunities inferred from them. Scale with E2E_PERF_SCALE.

func perfScale() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("E2E_PERF_SCALE"), 64); err == nil && v > 0 {
		return v
	}
	return 1
}

func scaled(n int) int {
	return max(1, int(math.Round(float64(n)*perfScale())))
}

func TestPerformance(t *testing.T) {
	if testing.Short() || image != "" {
		t.Skip("performance suite needs a local binary and is skipped in short mode")
	}
	s := startBinary(t, perfBinary)
	r := newReport(t, s)
	defer r.write()

	idle := r.scenarioIdleFootprint(scaled(2000))
	r.scenarioGroupFanOut(idle[:min(len(idle), 999)], scaled(100), 20)
	r.scenarioDirectMessages(idle[:min(len(idle), scaled(500))], 32, 5*time.Second)
	bulk := r.scenarioPersistence(scaled(6000), 32)
	r.scenarioHistoryReads(bulk, 32, 5*time.Second)
}

func (r *report) scenarioIdleFootprint(n int) []*tab {
	t, s := r.t, r.s
	time.Sleep(200 * time.Millisecond)
	before := r.sample()

	start := time.Now()
	tabs := s.openUsers(t, names("idle", n))
	elapsed := time.Since(start)
	time.Sleep(500 * time.Millisecond)
	after := r.sample()
	r.heapProfile("idle-footprint")

	sc := r.scenario("idle-footprint", fmt.Sprintf("%d users with one idle websocket each", n))
	sc.metric("connections", float64(n), "")
	sc.metric("connect rate", float64(n)/elapsed.Seconds(), "conn/s")
	sc.metric("rss per connection", float64(after.rssKB-before.rssKB)*1024/float64(n), "B")
	if after.heapInuse > 0 {
		sc.metric("heap in use per connection", float64(int64(after.heapInuse)-int64(before.heapInuse))/float64(n), "B")
		sc.metric("goroutines per connection", float64(after.goroutines-before.goroutines)/float64(n), "")
	}
	return tabs
}

func (r *report) scenarioGroupFanOut(members []*tab, messages, ratePerSecond int) {
	t, s := r.t, r.s
	name := "group-fanout"
	group := []string{"host"}
	for _, tb := range members {
		group = append(group, tb.user)
	}
	interval := time.Second / time.Duration(ratePerSecond)
	stopProfile := r.profile(name, time.Duration(messages)*interval)

	start := time.Now()
	for i := range messages {
		s.mustSend(t, "host", group, stamped(48))
		time.Sleep(time.Until(start.Add(time.Duration(i+1) * interval)))
	}
	latencies, _ := collectLatencies(t, members, messages)
	stopProfile()

	sc := r.scenario(name, fmt.Sprintf("%d messages at %d/s to a group of %d connected members", messages, ratePerSecond, len(members)))
	sc.latency(latencies)
	sc.metric("deliveries", float64(len(latencies)), "")
}

func (r *report) scenarioDirectMessages(receivers []*tab, workers int, duration time.Duration) {
	t, s := r.t, r.s
	name := "direct-message-throughput"
	r.allocBaseline(name)
	stopProfile := r.profile(name, duration)

	counts := make([]atomic.Int64, len(receivers))
	tokens := make([]string, len(receivers))
	for i := range receivers {
		tokens[i] = s.userToken(t, fmt.Sprint("dm-sender-", i))
	}
	var mu sync.Mutex
	var sendLatencies []time.Duration
	var rejected atomic.Int64
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	start := time.Now()
	for w := range workers {
		wg.Go(func() {
			var local []time.Duration
			for i := 0; time.Now().Before(deadline); i++ {
				p := (w*7919 + i) % len(receivers)
				body := sendBody([]string{fmt.Sprint("dm-sender-", p), receivers[p].user}, stamped(48))
				begin := time.Now()
				switch res := s.request(t, http.MethodPost, "/api/messages", tokens[p], body); res.status {
				case http.StatusAccepted:
					counts[p].Add(1)
					local = append(local, time.Since(begin))
				case http.StatusServiceUnavailable:
					rejected.Add(1)
					time.Sleep(5 * time.Millisecond)
				default:
					t.Errorf("send: %d", res.status)
				}
			}
			mu.Lock()
			sendLatencies = append(sendLatencies, local...)
			mu.Unlock()
		})
	}
	wg.Wait()
	sendElapsed := time.Since(start)

	var latencies []time.Duration
	var lastArrival time.Time
	for i, tb := range receivers {
		for _, d := range tb.deliveries(t, int(counts[i].Load()), 60*time.Second) {
			latencies = append(latencies, d.at.Sub(sentAt(t, d.data)))
			lastArrival = later(lastArrival, d.at)
		}
	}
	stopProfile()
	r.allocProfile(name)

	sc := r.scenario(name, fmt.Sprintf("%d workers sending flat out for %s across %d one-to-one conversations", workers, duration, len(receivers)))
	sc.metric("sends per second", float64(len(sendLatencies))/sendElapsed.Seconds(), "req/s")
	sc.metric("deliveries per second", float64(len(latencies))/lastArrival.Sub(start).Seconds(), "msg/s")
	sc.metric("rejected with 503", float64(rejected.Load()), "")
	sc.distribution("send request latency", sendLatencies)
	sc.latency(latencies)
}

// scenarioPersistence measures how fast accepted messages reach MongoDB. Sends
// rejected with 503 are retried, so the intake rate shows the backpressure.
func (r *report) scenarioPersistence(total, workers int) []string {
	t, s := r.t, r.s
	name := "persistence-throughput"
	channel := []string{"bulk-a", "bulk-b"}
	token := s.userToken(t, "bulk-a")
	stopProfile := r.profile(name, 3*time.Second)

	var rejected atomic.Int64
	var next atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for range workers {
		wg.Go(func() {
			for next.Add(1) <= int64(total) {
				for {
					res := s.request(t, http.MethodPost, "/api/messages", token, sendBody(channel, "bulk"))
					if res.status == http.StatusAccepted {
						break
					}
					if res.status != http.StatusServiceUnavailable {
						t.Errorf("send: %d", res.status)
						return
					}
					rejected.Add(1)
					time.Sleep(5 * time.Millisecond)
				}
			}
		})
	}
	wg.Wait()
	accepted := time.Since(start)
	for len(s.history(t, "bulk-a", channel, total-1, 1)) == 0 {
		if time.Since(start) > 2*time.Minute {
			t.Fatal("messages were never persisted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	persisted := time.Since(start)
	stopProfile()

	sc := r.scenario(name, fmt.Sprintf("%d messages from %d concurrent senders into one conversation", total, workers))
	sc.metric("accepted per second", float64(total)/accepted.Seconds(), "msg/s")
	sc.metric("persisted per second", float64(total)/persisted.Seconds(), "msg/s")
	sc.metric("rejected with 503", float64(rejected.Load()), "")
	return channel
}

func (r *report) scenarioHistoryReads(channel []string, workers int, duration time.Duration) {
	t, s := r.t, r.s
	name := "history-reads"
	token := s.userToken(t, channel[0])
	path := historyPath(channel, 0, 50)
	stopProfile := r.profile(name, duration)

	var mu sync.Mutex
	var latencies []time.Duration
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	start := time.Now()
	for range workers {
		wg.Go(func() {
			var local []time.Duration
			for time.Now().Before(deadline) {
				begin := time.Now()
				if res := s.request(t, http.MethodGet, path, token, nil); res.status != http.StatusOK {
					t.Errorf("history: %d", res.status)
					return
				}
				local = append(local, time.Since(begin))
			}
			mu.Lock()
			latencies = append(latencies, local...)
			mu.Unlock()
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	stopProfile()

	sc := r.scenario(name, fmt.Sprintf("%d concurrent readers fetching the newest 50 messages for %s", workers, duration))
	sc.metric("reads per second", float64(len(latencies))/elapsed.Seconds(), "req/s")
	sc.distribution("read latency", latencies)
}

func stamped(size int) string {
	head := fmt.Sprintf("t=%d;", time.Now().UnixNano())
	return head + strings.Repeat("x", max(0, size-len(head)))
}

// sentAt reads the send timestamp that stamped placed at the start of the
// message content without decoding the whole frame.
func sentAt(t testing.TB, frame []byte) time.Time {
	_, rest, ok := bytes.Cut(frame, []byte(`"content":"t=`))
	end := bytes.IndexByte(rest, ';')
	if !ok || end < 0 {
		t.Fatalf("frame without timestamp: %.120s", frame)
	}
	nanos, err := strconv.ParseInt(string(rest[:end]), 10, 64)
	if err != nil {
		t.Fatalf("bad timestamp in %.120s", frame)
	}
	return time.Unix(0, nanos)
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func collectLatencies(t *testing.T, tabs []*tab, messages int) ([]time.Duration, time.Time) {
	t.Helper()
	latencies := make([]time.Duration, 0, len(tabs)*messages)
	var lastArrival time.Time
	for _, tb := range tabs {
		for _, d := range tb.deliveries(t, messages, 60*time.Second) {
			latencies = append(latencies, d.at.Sub(sentAt(t, d.data)))
			lastArrival = later(lastArrival, d.at)
		}
	}
	return latencies, lastArrival
}
