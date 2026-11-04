package warp

import (
	"context"
	"net/netip"
	"net/url"
	"time"
)

// The types below exist only for the temporary warpprobe command. They let a
// diagnostic run register one device and test candidate endpoints with the
// exact production tunnel code.

type debugLogger struct{}

func (debugLogger) Debugf(string, ...interface{}) {}
func (debugLogger) Infof(string, ...interface{})  {}
func (debugLogger) Warnf(string, ...interface{})  {}
func (debugLogger) Errorf(string, ...interface{}) {}

// DebugResult is one endpoint probe result.
type DebugResult struct {
	Endpoint string
	Egress   string
	Err      error
}

// DebugProbeSession registers one device through proxy and probes every
// endpoint with a fresh tunnel, reporting the first successful egress.
func DebugProbeSession(ctx context.Context, proxy *url.URL, endpoints []string) ([]DebugResult, error) {
	log := debugLogger{}
	reg, err := newRegistrar(log, proxy, false).register(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]DebugResult, 0, len(endpoints))
	for _, endpoint := range endpoints {
		attemptCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		reg.Endpoint = endpoint
		reg.Endpoints = []string{endpoint}
		e, err := newExitWithStack(attemptCtx, reg, StackIPv6, netip.Addr{}, log, false)
		cancel()
		if err != nil {
			results = append(results, DebugResult{Endpoint: endpoint, Err: err})
		} else {
			results = append(results, DebugResult{Endpoint: endpoint, Egress: e.egress.String()})
			e.close()
		}
		if ctx.Err() != nil {
			break
		}
	}
	return results, nil
}
