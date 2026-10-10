package noderegistrar

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tazzledazzle/meridian/internal/awscall"
)

// EC2Fleet lists instances with DescribeInstances through the aws-gateway,
// signed with the platform credentials, exactly as any other AWS client would.
type EC2Fleet struct {
	Endpoint string // gateway URL
	Region   string
	Access   string
	Secret   string
	// ClusterTag selects the instances that belong to this cluster. Karpenter
	// tags every instance it launches with kubernetes.io/cluster/<name>=owned.
	ClusterTag string // tag key, e.g. kubernetes.io/cluster/meridian-g0
	Client     *http.Client
}

type describeInstancesOut struct {
	Instances []struct {
		ID    string `xml:"instanceId"`
		State string `xml:"instanceState>name"`
		Type  string `xml:"instanceType"`
		AZ    string `xml:"placement>availabilityZone"`
		DNS   string `xml:"privateDnsName"`
		IP    string `xml:"privateIpAddress"`
		Tags  []struct {
			Key   string `xml:"key"`
			Value string `xml:"value"`
		} `xml:"tagSet>item"`
	} `xml:"reservationSet>item>instancesSet>item"`
}

// Instances implements Fleet.
func (f *EC2Fleet) Instances(ctx context.Context) ([]Instance, error) {
	// Only instances carrying the cluster tag key belong to us.
	params := []string{"Filter.1.Name=tag-key", "Filter.1.Value.1=" + f.ClusterTag}
	req, _, err := awscall.BuildQuery(f.Endpoint, "ec2", f.Region, "DescribeInstances", "2016-11-15", params, f.Access, f.Secret, time.Now())
	if err != nil {
		return nil, err
	}
	c := f.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DescribeInstances: HTTP %d: %.200s", resp.StatusCode, body)
	}
	var out describeInstancesOut
	if err := xml.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("DescribeInstances: bad XML: %w", err)
	}
	res := make([]Instance, 0, len(out.Instances))
	for _, i := range out.Instances {
		tags := map[string]string{}
		for _, t := range i.Tags {
			tags[t.Key] = t.Value
		}
		res = append(res, Instance{ID: i.ID, State: i.State, Type: i.Type, AZ: i.AZ, PrivateDNS: i.DNS, PrivateIP: i.IP, Tags: tags})
	}
	return res, nil
}
