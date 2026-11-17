package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"opencode2api/internal/warp"
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	mode := "proxy"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	proxyAddr := "socks5://127.0.0.1:7897"
	if len(os.Args) > 2 {
		proxyAddr = os.Args[2]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	stack, err := warp.ParseStack("ipv6")
	if err != nil {
		panic(err)
	}
	opts := warp.Options{Size: 1, Stack: stack}
	switch mode {
	case "direct":
	case "regproxy":
		registry, _ := url.Parse(proxyAddr)
		opts.RegistryProxy = registry
	case "proxy":
		registry, _ := url.Parse(proxyAddr)
		opts.RegistryProxy = registry
		opts.SocksUDP = proxyAddr
	}
	fmt.Printf("mode=%s\n", mode)
	pool, err := warp.NewWithOptions(ctx, nil, opts)
	if err != nil {
		fmt.Println("pool error:", err)
		os.Exit(1)
	}
	defer pool.Close()
	snap := pool.Snapshot()
	fmt.Printf("egress=%v active=%d standby=%d cooling=%d\n", snap.Egress, snap.Active, snap.Standby, snap.Cooling)

	lease, err := pool.AcquireFor(ctx, "")
	if err != nil {
		fmt.Println("acquire error:", err)
		os.Exit(1)
	}
	defer lease.Release()
	fmt.Println("lease egress:", lease.EgressKey())

	dial := lease.DialContext
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dial(ctx, network, addr)
		},
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{Transport: tr, Timeout: 60 * time.Second}

	if req, err := http.NewRequestWithContext(ctx, "GET", "https://api64.ipify.org?format=json", nil); err == nil {
		if resp, err := client.Do(req); err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			fmt.Printf("ipify: %d %s\n", resp.StatusCode, strings.TrimSpace(string(b)))
		} else {
			fmt.Println("ipify error:", err)
		}
	}

	body, err := os.ReadFile("probe-run/body-deepseek-v4.1-flash-free.json")
	if err != nil {
		panic(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		panic(err)
	}
	raw, _ := json.Marshal(payload)
	project := randHex(20)
	session := "ses_" + randHex(12)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://opencode.ai/inference/openai/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Authorization", "Bearer public")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/latest/2.1.1/cli")
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-project", project)
	req.Header.Set("x-session-affinity", session)
	req.Header.Set("X-Session-Id", session)
	req.Header.Set("x-opencode-session-id", session)
	req.Header.Set("x-opencode-session", session)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("chat request error: %v\n", err)
		return
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	resp.Body.Close()
	fmt.Printf("chat status=%d\n", resp.StatusCode)
	for _, h := range []string{"Retry-After", "Cf-Placement", "CF-RAY"} {
		if v := resp.Header.Get(h); v != "" {
			fmt.Printf("  %s: %s\n", h, v)
		}
	}
	fmt.Printf("  body: %s\n", strings.TrimSpace(string(b)))
}
