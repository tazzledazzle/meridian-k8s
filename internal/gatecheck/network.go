package gatecheck

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type netInspect struct {
	Name       string                           `json:"Name"`
	Internal   bool                             `json:"Internal"`
	Containers map[string]struct{ Name string } `json:"Containers"`
}

type ctrInspect struct {
	Name       string `json:"Name"`
	HostConfig struct {
		PortBindings map[string][]struct{ HostIp, HostPort string } `json:"PortBindings"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage             `json:"Networks"`
		Ports    map[string][]struct{ HostPort string } `json:"Ports"`
	} `json:"NetworkSettings"`
	Mounts []struct{ Source, Destination string } `json:"Mounts"`
}

// exposesRuntime reports whether a mount path is a runtime socket (docker.sock, podman.sock, containerd.sock,
// docker.proxy.sock, ... : anything ending in .sock) or, for a bind source, a directory that holds one. Each of
// those is root-equivalent on the host's daemon. Destinations like /run are fine (tmpfs), so only sources are
// checked for parent directories.
func exposesRuntime(path string, isSource bool) bool {
	if path == "" {
		return false
	}
	p := filepath.Clean(path)
	if strings.HasSuffix(p, ".sock") {
		return true
	}
	if isSource {
		switch p {
		case "/", "/var", "/var/run", "/run":
			return true
		}
	}
	return false
}

// CheckNetwork audits the isolation of the gate stack from `docker network inspect` and `docker inspect <every
// container on the network>` output (amendments A1 and A5). It fails unless:
//   - the network is internal (Docker gives its containers no way out);
//   - every container on it is attached to that network ONLY (a second network could be a way out);
//   - no container publishes a port to the host (Docker opened none), and none is even configured to publish on
//     anything but loopback;
//   - no container mounts a container-runtime socket or a directory holding one (root-equivalent on the daemon);
//   - every container is attached to the audited network (inconsistent inspect data is not accepted);
//   - every container the network lists was inspected, so none escapes the audit.
func CheckNetwork(network, networkJSON, containersJSON string) error {
	var nets []netInspect
	if err := json.Unmarshal([]byte(networkJSON), &nets); err != nil || len(nets) != 1 {
		return fmt.Errorf("network inspect output is not a one-element JSON array (%v)", err)
	}
	var ctrs []ctrInspect
	if err := json.Unmarshal([]byte(containersJSON), &ctrs); err != nil {
		return fmt.Errorf("container inspect output is not JSON: %w", err)
	}
	n := nets[0]
	var problems []string
	if n.Name != network {
		problems = append(problems, fmt.Sprintf("inspected network is %q, want %q", n.Name, network))
	}
	if !n.Internal {
		problems = append(problems, fmt.Sprintf("network %s is not internal: containers on it can reach the internet", network))
	}
	if len(n.Containers) == 0 {
		problems = append(problems, fmt.Sprintf("network %s has no containers, so there is nothing to audit", network))
	}
	seen := map[string]bool{}
	for _, c := range ctrs {
		name := strings.TrimPrefix(c.Name, "/")
		seen[name] = true
		var nets []string
		for k := range c.NetworkSettings.Networks {
			if k != network {
				nets = append(nets, k)
			}
		}
		sort.Strings(nets)
		if len(nets) > 0 {
			problems = append(problems, fmt.Sprintf("container %s is also attached to %s", name, strings.Join(nets, ", ")))
		}
		// Configured bindings: k3d asks Docker to publish the API port even on an internal network, where Docker does
		// not open it. That is harmless there but would expose the API on every interface on any other network, so
		// only a loopback request is tolerated.
		for port, binds := range c.HostConfig.PortBindings {
			for _, b := range binds {
				if b.HostIp != "127.0.0.1" && b.HostIp != "::1" {
					problems = append(problems, fmt.Sprintf("container %s is configured to publish %s on all interfaces (HostIp %q)", name, port, b.HostIp))
				}
			}
		}
		for port, binds := range c.NetworkSettings.Ports {
			if len(binds) > 0 {
				problems = append(problems, fmt.Sprintf("container %s publishes %s to the host", name, port))
			}
		}
		if _, attached := c.NetworkSettings.Networks[network]; !attached {
			problems = append(problems, fmt.Sprintf("container %s is not attached to %s", name, network))
		}
		for _, m := range c.Mounts {
			if exposesRuntime(m.Source, true) || exposesRuntime(m.Destination, false) {
				problems = append(problems, fmt.Sprintf("container %s mounts a container-runtime socket or a directory holding one (docker.sock, %s -> %s)", name, m.Source, m.Destination))
			}
		}
	}
	var missing []string
	for _, c := range n.Containers {
		if !seen[c.Name] {
			missing = append(missing, c.Name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		problems = append(problems, "containers on the network that were not audited: "+strings.Join(missing, ", "))
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		return fmt.Errorf("isolation audit failed:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}
