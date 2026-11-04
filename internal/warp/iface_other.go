//go:build !windows

package warp

import "syscall"

// interfaceControl is a no-op outside Windows: only Windows routes by
// destination even when a socket is bound to a local address, so only there
// does a TUN steal the WireGuard traffic. Linux deployments that need a
// specific egress use the configured outer source prefix instead.
func interfaceControl(string, logger) func(string, string, syscall.RawConn) error { return nil }

// outerInterfacePinned reports whether WARP sockets are pinned to a physical
// adapter, which only happens on Windows.
func outerInterfacePinned() bool { return false }
