// Command streambench opens many idle Server-Sent Events connections and
// reports the server's resident-memory growth per connection.
//
// It talks to a running API, so it measures the real HTTP stack rather than a
// synthetic hub benchmark. Pass a JWT directly or credentials to log in; the
// stream token endpoint requires authentication for every connection.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type config struct {
	baseURL     string
	jwt         string
	email       string
	password    string
	connections int
	concurrency int
	hold        time.Duration
	serverPID   int
}

func main() {
	var cfg config
	flag.StringVar(&cfg.baseURL, "base-url", "http://localhost:8080", "API base URL")
	flag.StringVar(&cfg.jwt, "jwt", "", "access token (optional if email/password are given)")
	flag.StringVar(&cfg.email, "email", "", "login email")
	flag.StringVar(&cfg.password, "password", "", "login password")
	flag.IntVar(&cfg.connections, "connections", 1000, "number of idle SSE connections")
	flag.IntVar(&cfg.concurrency, "concurrency", 50, "parallel connection attempts")
	flag.DurationVar(&cfg.hold, "hold", 10*time.Second, "how long to keep connections open")
	flag.IntVar(&cfg.serverPID, "server-pid", 0, "API process id for /proc RSS measurement (0 disables)")
	flag.Parse()

	cfg.baseURL = strings.TrimRight(cfg.baseURL, "/")
	if cfg.concurrency < 1 {
		cfg.concurrency = 1
	}

	client := &http.Client{Timeout: 15 * time.Second}

	if cfg.jwt == "" {
		if cfg.email == "" || cfg.password == "" {
			fatal("-jwt or -email/-password is required")
		}
		token, err := login(client, cfg.baseURL, cfg.email, cfg.password)
		if err != nil {
			fatal("login: %v", err)
		}
		cfg.jwt = token
	}

	before := rssKB(cfg.serverPID)
	start := time.Now()

	var (
		mu     sync.Mutex
		bodies []io.Closer
		errs   []error
		sem    = make(chan struct{}, cfg.concurrency)
		wg     sync.WaitGroup
	)

	for i := 0; i < cfg.connections; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			body, err := openStream(client, cfg.baseURL, cfg.jwt)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			bodies = append(bodies, body)
		}()
	}
	wg.Wait()

	elapsed := time.Since(start)
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "opened %d/%d connections, first error: %v\n", len(bodies), cfg.connections, errs[0])
		closeAll(bodies)
		os.Exit(1)
	}

	// Let the server settle before sampling RSS.
	time.Sleep(500 * time.Millisecond)
	after := rssKB(cfg.serverPID)

	fmt.Printf("connections:   %d\n", len(bodies))
	fmt.Printf("open time:     %s (%.0f conn/s)\n", elapsed.Round(time.Millisecond), float64(len(bodies))/elapsed.Seconds())
	if cfg.serverPID > 0 {
		delta := after - before
		fmt.Printf("rss before:    %d kB\n", before)
		fmt.Printf("rss after:     %d kB\n", after)
		fmt.Printf("rss delta:     %d kB\n", delta)
		if len(bodies) > 0 {
			fmt.Printf("per connection: %.2f kB\n", float64(delta)/float64(len(bodies)))
		}
	}

	if cfg.hold > 0 {
		fmt.Printf("holding open for %s...\n", cfg.hold)
		time.Sleep(cfg.hold)
	}
	closeAll(bodies)
}

func login(client *http.Client, baseURL, email, password string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := client.Post(baseURL+"/v1/auth/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("empty access token")
	}
	return out.AccessToken, nil
}

func openStream(client *http.Client, baseURL, jwt string) (io.Closer, error) {
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/stream-tokens", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("stream token status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokenResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}
	if tokenResp.Token == "" {
		return nil, fmt.Errorf("empty stream token")
	}

	streamURL := baseURL + "/v1/stream?token=" + url.QueryEscape(tokenResp.Token)
	streamReq, err := http.NewRequest(http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, err
	}
	// SSE responses are long-lived; a client timeout would close them.
	streamClient := &http.Client{Transport: client.Transport}
	streamResp, err := streamClient.Do(streamReq)
	if err != nil {
		return nil, err
	}
	if streamResp.StatusCode != http.StatusOK {
		streamResp.Body.Close()
		return nil, fmt.Errorf("stream status %d", streamResp.StatusCode)
	}

	// Drain frames so the server's kernel buffer never fills; an idle client
	// that does not read is a different (drop-slow-consumer) scenario.
	go func() { _, _ = io.Copy(io.Discard, streamResp.Body) }()
	return streamResp.Body, nil
}

func rssKB(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return value
	}
	return 0
}

func closeAll(bodies []io.Closer) {
	for _, body := range bodies {
		_ = body.Close()
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
