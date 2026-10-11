package gatecheck

import (
	"encoding/json"
	"strings"
	"testing"
)

const goodNet = `[{"Name":"meridian-gate","Internal":true,"Containers":{"a":{"Name":"floci"},"b":{"Name":"aws-gateway"},"c":{"Name":"k3d-server"}}}]`

// container renders `docker inspect` output. bindings is what was configured (HostConfig.PortBindings); realized is
// what Docker actually opened (NetworkSettings.Ports).
func container(name, networks, bindings, mounts string) string {
	return containerPorts(name, networks, bindings, bindings, mounts)
}

func containerPorts(name, networks, bindings, realized, mounts string) string {
	return `{"Name":"/` + name + `","HostConfig":{"PortBindings":` + bindings + `},"NetworkSettings":{"Networks":` + networks + `,"Ports":` + realized + `},"Mounts":` + mounts + `}`
}

func good() string {
	return "[" + strings.Join([]string{
		container("floci", `{"meridian-gate":{}}`, `{"4566/tcp":null}`, `[]`),
		container("aws-gateway", `{"meridian-gate":{}}`, `{}`, `[{"Source":"/Users/x/trace/gate"}]`),
		container("k3d-server", `{"meridian-gate":{}}`, `{}`, `[{"Source":"/var/lib/docker/volumes/v"}]`),
	}, ",") + "]"
}

func TestHealthyGateNetworkPasses(t *testing.T) {
	if err := CheckNetwork("meridian-gate", goodNet, good()); err != nil {
		t.Fatal(err)
	}
}

func TestNotInternalFails(t *testing.T) {
	err := CheckNetwork("meridian-gate", strings.Replace(goodNet, `"Internal":true`, `"Internal":false`, 1), good())
	if err == nil || !strings.Contains(err.Error(), "internal") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecondNetworkFails(t *testing.T) {
	bad := strings.Replace(good(), `"floci","HostConfig"`, `"floci","HostConfig"`, 1)
	bad = strings.Replace(bad, `{"meridian-gate":{}}`, `{"meridian-gate":{},"bridge":{}}`, 1)
	err := CheckNetwork("meridian-gate", goodNet, bad)
	if err == nil || !strings.Contains(err.Error(), "bridge") {
		t.Fatalf("a container with a route to the bridge network was accepted: %v", err)
	}
}

func TestPublishedPortFails(t *testing.T) {
	bad := strings.Replace(good(), `"4566/tcp":null`, `"4566/tcp":[{"HostIp":"127.0.0.1","HostPort":"4566"}]`, 2)
	err := CheckNetwork("meridian-gate", goodNet, bad)
	if err == nil || !strings.Contains(err.Error(), "publishes") {
		t.Fatalf("err = %v", err)
	}
}

func TestExposedButUnpublishedPortPasses(t *testing.T) {
	if err := CheckNetwork("meridian-gate", goodNet, good()); err != nil { // floci has "4566/tcp": null
		t.Fatal(err)
	}
}

func TestDockerSocketMountFails(t *testing.T) {
	bad := strings.Replace(good(), `/var/lib/docker/volumes/v`, `/var/run/docker.sock`, 1)
	err := CheckNetwork("meridian-gate", goodNet, bad)
	if err == nil || !strings.Contains(err.Error(), "docker.sock") {
		t.Fatalf("err = %v", err)
	}
}

func TestSocketAtAnyPathFails(t *testing.T) {
	for _, src := range []string{"/run/docker.sock", "/var/run/docker.sock", "/Users/x/.docker/run/docker.sock"} {
		bad := strings.Replace(good(), `/var/lib/docker/volumes/v`, src, 1)
		if CheckNetwork("meridian-gate", goodNet, bad) == nil {
			t.Errorf("accepted a mount of %s", src)
		}
	}
}

func TestContainerNotInspectedFails(t *testing.T) {
	// The network lists three containers; inspecting only two would leave one unchecked.
	var all []json.RawMessage
	_ = json.Unmarshal([]byte(good()), &all)
	short, _ := json.Marshal(all[:2])
	if err := CheckNetwork("meridian-gate", goodNet, string(short)); err == nil {
		t.Fatal("accepted an audit that skipped a container on the network")
	}
}

func TestEmptyNetworkFails(t *testing.T) {
	if err := CheckNetwork("meridian-gate", `[{"Name":"meridian-gate","Internal":true,"Containers":{}}]`, `[]`); err == nil {
		t.Fatal("an empty network proves nothing")
	}
}

func TestBadJSONFails(t *testing.T) {
	if CheckNetwork("meridian-gate", "{", good()) == nil || CheckNetwork("meridian-gate", goodNet, "{") == nil {
		t.Fatal("accepted malformed JSON")
	}
}

func withServer(c string) string {
	return "[" + strings.Join([]string{
		container("floci", `{"meridian-gate":{}}`, `{"4566/tcp":null}`, `[]`),
		container("aws-gateway", `{"meridian-gate":{}}`, `{}`, `[]`),
		c,
	}, ",") + "]"
}

// k3d asks Docker to publish the API port even on an internal network, where Docker silently does not open it.
func TestConfiguredButUnrealizedAllInterfacesBindingFails(t *testing.T) {
	c := containerPorts("k3d-server", `{"meridian-gate":{}}`, `{"6443/tcp":[{"HostIp":"0.0.0.0","HostPort":"62762"}]}`, `{"6443/tcp":[]}`, `[]`)
	err := CheckNetwork("meridian-gate", goodNet, withServer(c))
	if err == nil || !strings.Contains(err.Error(), "all interfaces") {
		t.Fatalf("a binding that would expose the API on every interface was accepted: %v", err)
	}
}

func TestEmptyHostIPCountsAsAllInterfaces(t *testing.T) {
	c := containerPorts("k3d-server", `{"meridian-gate":{}}`, `{"6443/tcp":[{"HostIp":"","HostPort":"1"}]}`, `{}`, `[]`)
	if CheckNetwork("meridian-gate", goodNet, withServer(c)) == nil {
		t.Fatal("an empty HostIp means every interface")
	}
}

func TestLoopbackOnlyUnrealizedBindingPasses(t *testing.T) {
	c := containerPorts("k3d-server", `{"meridian-gate":{}}`, `{"6443/tcp":[{"HostIp":"127.0.0.1","HostPort":"62762"}]}`, `{"6443/tcp":[]}`, `[]`)
	if err := CheckNetwork("meridian-gate", goodNet, withServer(c)); err != nil {
		t.Fatalf("a loopback-only request that Docker did not open must pass: %v", err)
	}
}

func TestRealizedLoopbackPortStillFails(t *testing.T) {
	c := containerPorts("k3d-server", `{"meridian-gate":{}}`, `{"6443/tcp":[{"HostIp":"127.0.0.1","HostPort":"62762"}]}`, `{"6443/tcp":[{"HostIp":"127.0.0.1","HostPort":"62762"}]}`, `[]`)
	err := CheckNetwork("meridian-gate", goodNet, withServer(c))
	if err == nil || !strings.Contains(err.Error(), "publishes") {
		t.Fatalf("a port Docker really opened must fail: %v", err)
	}
}
