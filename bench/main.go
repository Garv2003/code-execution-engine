package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type config struct {
	url         string
	lang        string
	concurrency int
	duration    time.Duration
	requests    int
	apiKey      string
}

type submitResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type resultEvent struct {
	Status   *string `json:"status"`
	ID       string  `json:"id"`
	ExitCode int     `json:"exit_code"`
	Timeout  bool    `json:"timeout"`
	OOM      bool    `json:"oom"`
}

type sample struct {
	latency time.Duration
	ok      bool
}

var samplePrograms = map[string]string{
	"python":     "print(sum(range(1000)))",
	"golang":     "package main\nimport \"fmt\"\nfunc main(){s:=0\nfor i:=0;i<1000;i++{s+=i}\nfmt.Println(s)}",
	"javascript": "let s=0;for(let i=0;i<1000;i++)s+=i;console.log(s)",
	"ruby":       "puts (0...1000).sum",
	"php":        "<?php echo array_sum(range(0,999));",
	"bash":       "echo $((999*1000/2))",
	"perl":       "my $s=0;$s+=$_ for 0..999;print $s;",
	"lua":        "local s=0 for i=0,999 do s=s+i end print(s)",
}

func programFor(lang string) string {
	if p, ok := samplePrograms[lang]; ok {
		return p
	}
	// Fall back to python; the -lang flag lets the caller override.
	return samplePrograms["python"]
}

func main() {
	cfg := parseFlags()

	client := &http.Client{Timeout: 60 * time.Second}
	code := programFor(cfg.lang)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "interrupted, draining in-flight requests...")
		cancel()
	}()

	if cfg.duration > 0 {
		timerCtx, timerCancel := context.WithTimeout(ctx, cfg.duration)
		defer timerCancel()
		ctx = timerCtx
	}

	var (
		mu        sync.Mutex
		samples   []sample
		issued    int64
		wg        sync.WaitGroup
		remaining int64 = int64(cfg.requests)
	)

	start := time.Now()
	for i := 0; i < cfg.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				if cfg.requests > 0 {
					if atomic.AddInt64(&remaining, -1) < 0 {
						return
					}
				}
				atomic.AddInt64(&issued, 1)

				reqStart := time.Now()
				ok := runOnce(ctx, client, cfg, code)
				lat := time.Since(reqStart)

				mu.Lock()
				samples = append(samples, sample{latency: lat, ok: ok})
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	report(samples, elapsed, cfg)
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.url, "url", "http://localhost:8080", "base URL of the code-execution-engine API")
	flag.StringVar(&cfg.lang, "lang", "python", "language id to submit (must exist in languages.json)")
	flag.IntVar(&cfg.concurrency, "concurrency", 10, "number of concurrent workers")
	flag.DurationVar(&cfg.duration, "duration", 0, "how long to run (e.g. 30s); mutually exclusive with -requests")
	flag.IntVar(&cfg.requests, "requests", 0, "total number of requests to send; mutually exclusive with -duration")
	flag.StringVar(&cfg.apiKey, "api-key", "", "API key sent via the X-API-Key header (optional)")
	flag.Parse()

	if cfg.concurrency <= 0 {
		fmt.Fprintln(os.Stderr, "-concurrency must be > 0")
		os.Exit(2)
	}
	if cfg.duration <= 0 && cfg.requests <= 0 {
		fmt.Fprintln(os.Stderr, "provide either -duration or -requests")
		os.Exit(2)
	}
	if cfg.duration > 0 && cfg.requests > 0 {
		fmt.Fprintln(os.Stderr, "-duration and -requests are mutually exclusive")
		os.Exit(2)
	}
	cfg.url = strings.TrimRight(cfg.url, "/")
	return cfg
}

// runOnce performs a full submit -> stream-result cycle and reports success.
func runOnce(ctx context.Context, client *http.Client, cfg config, code string) bool {
	id, err := submit(ctx, client, cfg, code)
	if err != nil {
		return false
	}
	return streamResult(ctx, client, cfg, id)
}

func submit(ctx context.Context, client *http.Client, cfg config, code string) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"language": cfg.lang,
		"code":     code,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.url+"/submit", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.apiKey != "" {
		req.Header.Set("X-API-Key", cfg.apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("submit returned status %d", resp.StatusCode)
	}

	var sr submitResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", err
	}
	if sr.ID == "" {
		return "", fmt.Errorf("submit returned empty job id")
	}
	return sr.ID, nil
}

// streamResult opens the SSE result stream and reads until the terminal result
// event arrives (the one carrying an exit code, not the "subscribed" ack).
func streamResult(ctx context.Context, client *http.Client, cfg config, id string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.url+"/result/"+id, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "text/event-stream")
	if cfg.apiKey != "" {
		req.Header.Set("X-API-Key", cfg.apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		var ev resultEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		// Skip the "subscribed" acknowledgement event; keep reading until the
		// real result event (no top-level "status" field) arrives.
		if ev.Status != nil {
			continue
		}
		// Terminal result event. Treat a clean exit as success.
		return ev.ExitCode == 0 && !ev.Timeout && !ev.OOM
	}
	// Stream ended without a result event (error, disconnect, or ctx cancel).
	return false
}

func report(samples []sample, elapsed time.Duration, cfg config) {
	total := len(samples)
	if total == 0 {
		fmt.Println("no requests completed")
		return
	}

	var failures int
	latencies := make([]time.Duration, 0, total)
	for _, s := range samples {
		if !s.ok {
			failures++
		}
		latencies = append(latencies, s.latency)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	throughput := float64(total) / elapsed.Seconds()
	errorRate := float64(failures) / float64(total) * 100

	fmt.Println("=== code-execution-engine load test ===")
	fmt.Printf("target:        %s\n", cfg.url)
	fmt.Printf("language:      %s\n", cfg.lang)
	fmt.Printf("concurrency:   %d\n", cfg.concurrency)
	fmt.Printf("wall time:     %s\n", elapsed.Round(time.Millisecond))
	fmt.Println("---------------------------------------")
	fmt.Printf("total requests: %d\n", total)
	fmt.Printf("successes:      %d\n", total-failures)
	fmt.Printf("failures:       %d\n", failures)
	fmt.Printf("error rate:     %.2f%%\n", errorRate)
	fmt.Printf("throughput:     %.2f req/s\n", throughput)
	fmt.Println("--- end-to-end latency ---")
	fmt.Printf("p50:            %s\n", percentile(latencies, 50).Round(time.Millisecond))
	fmt.Printf("p95:            %s\n", percentile(latencies, 95).Round(time.Millisecond))
	fmt.Printf("p99:            %s\n", percentile(latencies, 99).Round(time.Millisecond))
	fmt.Printf("min:            %s\n", latencies[0].Round(time.Millisecond))
	fmt.Printf("max:            %s\n", latencies[len(latencies)-1].Round(time.Millisecond))
}

// percentile returns the p-th percentile from a pre-sorted slice using the
// nearest-rank method.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
