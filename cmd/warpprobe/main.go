package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"opencode2api/internal/warp"
)

// Candidate WARP endpoints. The first groups test whether the block is
// address-scoped; the weird ports test whether it is port-scoped.
var candidates = []string{
	"188.114.96.1:2408",
	"188.114.97.1:2408",
	"188.114.98.1:2408",
	"188.114.99.1:2408",
	"162.159.193.1:2408",
	"162.159.195.1:2408",
	"162.159.204.1:2408",
	"162.159.192.1:854",
	"162.159.192.1:859",
	"162.159.192.1:880",
	"162.159.192.1:890",
	"162.159.192.1:894",
	"162.159.192.1:943",
	"162.159.192.1:946",
	"162.159.192.1:955",
	"162.159.192.1:963",
	"162.159.192.1:970",
	"162.159.192.1:980",
	"162.159.192.1:990",
	"188.114.96.1:854",
	"188.114.96.1:880",
	"162.159.193.1:880",
}

func main() {
	proxyRaw := "http://127.0.0.1:10808"
	if len(os.Args) > 1 {
		proxyRaw = os.Args[1]
	}
	var proxy *url.URL
	if proxyRaw != "none" {
		parsed, err := url.Parse(proxyRaw)
		if err != nil {
			fmt.Println("bad proxy:", err)
			os.Exit(2)
		}
		proxy = parsed
	}
	endpoints := candidates
	if len(os.Args) > 2 {
		endpoints = strings.Split(os.Args[2], ",")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	fmt.Printf("registering WARP device (proxy=%s), then probing %d endpoints\n", proxyRaw, len(endpoints))
	results, err := warp.DebugProbeSession(ctx, proxy, endpoints)
	if err != nil {
		fmt.Println("session error:", err)
	}
	ok := 0
	for _, r := range results {
		if r.Err != nil {
			msg := r.Err.Error()
			if len(msg) > 180 {
				msg = msg[:180] + "..."
			}
			fmt.Printf("FAIL %-24s %s\n", r.Endpoint, msg)
		} else {
			ok++
			fmt.Printf("OK   %-24s egress=%s\n", r.Endpoint, r.Egress)
		}
	}
	fmt.Printf("summary: %d/%d reachable\n", ok, len(results))
	if ok == 0 {
		os.Exit(1)
	}
}
