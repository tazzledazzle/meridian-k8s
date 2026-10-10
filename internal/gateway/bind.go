// Package gateway is the single AWS endpoint: it verifies SigV4, identifies the
// service and operation, enforces ops.yaml, and routes to a backend.
package gateway

import (
	"fmt"
	"net"
)

// CheckBind refuses to listen on anything but a loopback address unless the
// caller explicitly opts in. Inside a container the opt-in is normal (the
// container's network namespace is private and its published ports are bound to
// 127.0.0.1 by compose); on a bare host it exposes an AWS control plane to the LAN.
func CheckBind(addr string, allowAnyInterface bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Garbage stays an error even with the opt-in.
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if host == "localhost" { // the only hostname we accept; it is loopback by convention
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	if allowAnyInterface { // explicit opt-in, e.g. inside a container
		return nil
	}
	return fmt.Errorf("refusing to listen on %q: not a loopback address; pass -allow-any-interface only inside a container whose published ports are bound to 127.0.0.1", addr)
}
