// Command node-registrar creates a Kubernetes Node for every running simulated
// EC2 instance and deletes it when the instance is gone. It runs inside the
// cluster and uses the pod's service account.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tazzledazzle/meridian/internal/catalog"
	"github.com/tazzledazzle/meridian/internal/noderegistrar"
)

func main() {
	endpoint := flag.String("ec2-endpoint", "http://aws-gateway:4566", "aws-gateway URL")
	cluster := flag.String("cluster", "meridian-g0", "cluster name; selects instances tagged kubernetes.io/cluster/<name>")
	interval := flag.Duration("interval", 2*time.Second, "time between reconcile passes")
	def := flag.String("default-backend", "kwok", "backend for instances without a meridian.io/node-backend tag (only kwok is built in G0)")
	flag.Parse()

	// Credentials come from the environment (a Secret in the cluster), never from flags or files in the repo.
	access, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		log.Fatal("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required")
	}
	kube, err := noderegistrar.InCluster()
	if err != nil {
		log.Fatal(err)
	}
	r := &noderegistrar.Reconciler{
		Fleet: &noderegistrar.EC2Fleet{Endpoint: *endpoint, Region: catalog.Region, Access: access, Secret: secret,
			ClusterTag: "kubernetes.io/cluster/" + *cluster},
		Cluster:  kube,
		Backends: map[string]noderegistrar.Backend{"kwok": noderegistrar.KWOK{}},
		Default:  *def,
		Logf:     log.Printf,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("node-registrar started: cluster=%s endpoint=%s interval=%s", *cluster, *endpoint, *interval)
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		if _, err := r.Reconcile(ctx); err != nil && ctx.Err() == nil {
			log.Printf("reconcile: %v", err) // transient: the next pass retries
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
