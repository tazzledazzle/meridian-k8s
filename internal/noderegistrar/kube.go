package noderegistrar

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Kube is a minimal Kubernetes REST client for Nodes. It uses the pod's
// service account, so the registrar needs no client library.
type Kube struct {
	Base   string // https://10.43.0.1:443
	Token  string
	Client *http.Client
}

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// InCluster builds a client from the pod's service account.
func InCluster() (*Kube, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not running in a cluster (KUBERNETES_SERVICE_HOST/PORT unset)")
	}
	tok, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("no certificates in %s/ca.crt", saDir)
	}
	return &Kube{
		Base:  "https://" + host + ":" + port,
		Token: string(bytes.TrimSpace(tok)),
		Client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		},
	}, nil
}

func (k *Kube) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.Base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.Token) // the pod's service account token
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := k.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, b, nil
}

// Wire shapes: only the fields the registrar sets or reads.
type kTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

type kNode struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Metadata   struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels,omitempty"`
		Annotations map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Spec struct {
		ProviderID string   `json:"providerID,omitempty"`
		Taints     []kTaint `json:"taints,omitempty"`
	} `json:"spec"`
	Status struct {
		Capacity    map[string]string `json:"capacity,omitempty"`
		Allocatable map[string]string `json:"allocatable,omitempty"`
	} `json:"status"`
}

func toK(n Node) kNode {
	var k kNode
	k.APIVersion, k.Kind = "v1", "Node"
	k.Metadata.Name, k.Metadata.Labels, k.Metadata.Annotations = n.Name, n.Labels, n.Annotations
	k.Spec.ProviderID = n.ProviderID
	for _, t := range n.Taints {
		k.Spec.Taints = append(k.Spec.Taints, kTaint{t.Key, t.Value, t.Effect})
	}
	k.Status.Capacity, k.Status.Allocatable = n.Capacity, n.Allocatable
	return k
}

// ManagedNodes lists only Nodes carrying the registrar's label.
func (k *Kube) ManagedNodes(ctx context.Context) ([]Node, error) {
	// The label selector is the safety rail: unlabeled nodes (the control plane) never appear.
	sel := url.QueryEscape(ManagedByKey + "=" + ManagedByValue)
	code, body, err := k.do(ctx, http.MethodGet, "/api/v1/nodes?labelSelector="+sel, nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("list nodes: HTTP %d: %.200s", code, body)
	}
	var list struct {
		Items []kNode `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, Node{Name: it.Metadata.Name, ProviderID: it.Spec.ProviderID,
			Labels: it.Metadata.Labels, Annotations: it.Metadata.Annotations})
	}
	return out, nil
}

// Create makes the Node. A 409 means it already exists, which is success.
func (k *Kube) Create(ctx context.Context, n Node) error {
	code, body, err := k.do(ctx, http.MethodPost, "/api/v1/nodes", toK(n))
	if err != nil {
		return err
	}
	if code == http.StatusConflict {
		return nil
	}
	if code != http.StatusCreated && code != http.StatusOK {
		return fmt.Errorf("create node: HTTP %d: %.200s", code, body)
	}
	return nil
}

// Delete removes the Node. A 404 means it is already gone, which is success.
func (k *Kube) Delete(ctx context.Context, name string) error {
	code, body, err := k.do(ctx, http.MethodDelete, "/api/v1/nodes/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusAccepted && code != http.StatusNotFound {
		return fmt.Errorf("delete node: HTTP %d: %.200s", code, body)
	}
	return nil
}
