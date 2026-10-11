//go:build gate

package gate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	network = "meridian-gate" // the isolated network (deploy/compose.gate.yaml)
	gateway = "http://aws-gateway:4566"
	region  = "us-west-2"
	queue   = "meridian-g0-interruption"
)

// rig runs commands from the repository root with the local credentials in the environment.
type rig struct {
	root string
	env  []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	env := os.Environ()
	f, err := os.Open(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatalf("no .env: run `make init` (and `make gate-up`): %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && k != "" && !strings.HasPrefix(k, "#") {
			env = append(env, k+"="+v)
		}
	}
	return &rig{root: root, env: env}
}

// sh runs a command and returns its combined output. A non-zero exit is an error that carries the output.
func (r *rig) sh(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = r.root, r.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// kc runs kubectl inside the k3s server container (see scripts/kc.sh).
func (r *rig) kc(args ...string) (string, error) {
	return r.sh(60*time.Second, "./scripts/kc.sh", args...)
}

// kcJSON runs kubectl with -o json and decodes the answer into v.
func (r *rig) kcJSON(v any, args ...string) error {
	out, err := r.kc(append(args, "-o", "json")...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), v)
}

// tool runs one of the project's binaries in a container on the isolated network, as Karpenter's pod would reach
// the gateway: by name, signed with the local credentials (passed by name so they never appear in `ps`).
func (r *rig) tool(binary string, args ...string) (string, error) {
	base := []string{"run", "--rm", "--network", network, "-e", "AWS_ACCESS_KEY_ID", "-e", "AWS_SECRET_ACCESS_KEY",
		"-e", "AWS_REGION=" + region, "--entrypoint", "/usr/local/bin/" + binary, "meridian/tools:gate"}
	return r.sh(90*time.Second, "docker", append(base, args...)...)
}

// awsQuery calls an EC2/IAM/STS query action through the gateway.
func (r *rig) awsQuery(service, version, action string, params ...string) (string, error) {
	args := []string{"-endpoint", gateway, "-service", service, "-version", version, "-action", action}
	return r.tool("awscall", append(args, params...)...)
}

// eventually polls fn every 2 seconds until it reports done or the timeout passes; the last message is the failure.
func eventually(t *testing.T, timeout time.Duration, what string, fn func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "never ran"
	for {
		ok, msg := fn()
		if ok {
			return
		}
		last = msg
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s: %s", timeout, what, last)
		}
		time.Sleep(2 * time.Second)
	}
}

// Minimal views of the Kubernetes objects the criteria read.
type meta struct {
	Name              string            `json:"name"`
	Labels            map[string]string `json:"labels"`
	DeletionTimestamp string            `json:"deletionTimestamp"`
}

type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type node struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		ProviderID    string `json:"providerID"`
		Unschedulable bool   `json:"unschedulable"`
		Taints        []struct {
			Key string `json:"key"`
		} `json:"taints"`
	} `json:"spec"`
	Status struct {
		Conditions []condition `json:"conditions"`
	} `json:"status"`
}

func (n node) ready() bool {
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true
		}
	}
	return false
}

func (n node) tainted(key string) bool {
	for _, t := range n.Spec.Taints {
		if t.Key == key {
			return true
		}
	}
	return false
}

// instanceID extracts i-... from a providerID like aws:///us-west-2a/i-0123456789abcdef0.
func (n node) instanceID() string {
	i := strings.LastIndex(n.Spec.ProviderID, "/")
	return n.Spec.ProviderID[i+1:]
}

type nodeList struct {
	Items []node `json:"items"`
}

type pod struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type podList struct {
	Items []pod `json:"items"`
}

func (r *rig) nodesOf(pool string) ([]node, error) {
	var l nodeList
	err := r.kcJSON(&l, "get", "nodes", "-l", "karpenter.sh/nodepool="+pool)
	return l.Items, err
}

// runningPodOn reports whether an app=<app> pod is Running on the named node.
func (r *rig) runningPodOn(app, nodeName string) (bool, error) {
	var l podList
	if err := r.kcJSON(&l, "get", "pods", "-l", "app="+app); err != nil {
		return false, err
	}
	for _, p := range l.Items {
		if p.Spec.NodeName == nodeName && p.Status.Phase == "Running" {
			return true, nil
		}
	}
	return false, nil
}

// instanceState asks EC2 (through the gateway) for the state of one instance.
func (r *rig) instanceState(id string) (string, error) {
	out, err := r.awsQuery("ec2", "2016-11-15", "DescribeInstances", "InstanceId.1="+id)
	if err != nil {
		return "", err
	}
	const open, closeTag = "<instanceState>", "</instanceState>"
	i, j := strings.Index(out, open), strings.Index(out, closeTag)
	if i < 0 || j < i {
		return "", fmt.Errorf("no instanceState in %.200s", out)
	}
	seg := out[i:j]
	a, b := strings.Index(seg, "<name>"), strings.Index(seg, "</name>")
	if a < 0 || b < a {
		return "", fmt.Errorf("no state name in %.200s", seg)
	}
	return seg[a+len("<name>") : b], nil
}

func (r *rig) scale(t *testing.T, deployment string, n int) {
	t.Helper()
	if _, err := r.kc("scale", "deployment/"+deployment, fmt.Sprintf("--replicas=%d", n)); err != nil {
		t.Fatal(err)
	}
}

// kcRaw is kc with the exit status ignored: callers that expect a non-zero kubectl exit use it.
func (r *rig) kcOut(args ...string) string {
	out, _ := r.kc(args...)
	return out
}

// probeFrom runs `simctl probe url` from one vantage point and reports whether the URL answered. The vantage points
// are the places the gate promises have no route out (amendment A5): a container on the gate network, a container in
// Floci's own network namespace, a container in the Karpenter pod's namespace, and the Kubernetes node.
type vantage struct {
	name string
	run  func(r *rig, url string) (reachable bool, detail string, err error)
}

func simctlProbe(r *rig, networkArg, url string) (bool, string, error) {
	args := []string{"run", "--rm", "--network", networkArg, "--entrypoint", "/usr/local/bin/simctl", "meridian/tools:gate",
		"-timeout", "5s", "probe", url}
	out, err := r.sh(60*time.Second, "docker", args...)
	if err == nil {
		return true, strings.TrimSpace(out), nil
	}
	if strings.Contains(out, "unreachable:") {
		return false, strings.TrimSpace(out), nil
	}
	return false, out, err // the probe itself failed to run: not evidence of anything
}

var vantages = []vantage{
	{"a container on the gate network", func(r *rig, url string) (bool, string, error) { return simctlProbe(r, network, url) }},
	{"Floci's network namespace", func(r *rig, url string) (bool, string, error) {
		return simctlProbe(r, "container:meridian-gate-floci-1", url)
	}},
	{"the Karpenter pod's network namespace", func(r *rig, url string) (bool, string, error) {
		pod, err := r.kc("-n", "kube-system", "get", "pods", "-l", "app.kubernetes.io/name=karpenter", "-o", "jsonpath={.items[0].metadata.name}")
		if err != nil {
			return false, pod, err
		}
		// An ephemeral container shares the pod's network namespace; its name must be unique per probe.
		name := fmt.Sprintf("egress-%d", time.Now().UnixNano())
		if _, err := r.kc("-n", "kube-system", "debug", pod, "-q", "--profile=general", "--image=meridian/tools:gate",
			"--image-pull-policy=Never", "--target=controller", "-c", name, "--",
			"/usr/local/bin/simctl", "-timeout", "5s", "probe", url); err != nil {
			return false, "", err
		}
		var code string
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			code, _ = r.kc("-n", "kube-system", "get", "pod", pod, "-o",
				fmt.Sprintf(`jsonpath={.status.ephemeralContainerStatuses[?(@.name=="%s")].state.terminated.exitCode}`, name))
			if strings.TrimSpace(code) != "" {
				break
			}
			time.Sleep(time.Second)
		}
		logs, _ := r.kc("-n", "kube-system", "logs", pod, "-c", name)
		switch strings.TrimSpace(code) {
		case "0":
			return true, strings.TrimSpace(logs), nil
		case "1":
			return false, strings.TrimSpace(logs), nil
		}
		return false, logs, fmt.Errorf("probe container did not finish (exit code %q)", code)
	}},
	{"the Kubernetes node", func(r *rig, url string) (bool, string, error) {
		// The k3s image's BusyBox wget has no TLS, and the route is the same on port 80.
		url = strings.Replace(url, "https://", "http://", 1)
		out, err := r.sh(30*time.Second, "docker", "exec", "k3d-meridian-g0-server-0", "wget", "-T", "5", "-q", "-O", "/dev/null", url)
		// BusyBox wget exits non-zero on an HTTP error status too; an HTTP status line still proves a route.
		if err == nil || strings.Contains(out, "HTTP/") || strings.Contains(out, "server returned error") {
			return true, strings.TrimSpace(out), nil
		}
		if strings.Contains(out, "Network is unreachable") || strings.Contains(out, "Host is unreachable") ||
			strings.Contains(out, "bad address") || strings.Contains(out, "download timed out") || strings.Contains(out, "can't connect") {
			return false, strings.TrimSpace(out), nil
		}
		return false, out, err
	}},
}

// gateContainers returns the audit input for gatecheck.CheckNetwork: `docker network inspect` and `docker inspect`
// of every container on the network.
func (r *rig) gateContainers() (networkJSON, containersJSON string, err error) {
	networkJSON, err = r.sh(30*time.Second, "docker", "network", "inspect", network)
	if err != nil {
		return "", "", err
	}
	var nets []struct {
		Containers map[string]struct{ Name string } `json:"Containers"`
	}
	if err := json.Unmarshal([]byte(networkJSON), &nets); err != nil || len(nets) != 1 {
		return "", "", fmt.Errorf("network inspect: %v", err)
	}
	args := []string{"inspect"}
	for _, c := range nets[0].Containers {
		args = append(args, c.Name)
	}
	containersJSON, err = r.sh(30*time.Second, "docker", args...)
	return networkJSON, containersJSON, err
}
