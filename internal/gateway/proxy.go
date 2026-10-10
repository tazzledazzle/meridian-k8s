package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/tazzledazzle/meridian/internal/awsapi"
)

// backendTimeout bounds how long a backend may take to start answering.
const backendTimeout = 60 * time.Second

// Proxy returns a reverse proxy to a backend service. The incoming Authorization
// and Host headers are forwarded unchanged, because emulators such as Floci
// and Moto identify the service from the SigV4 credential scope.
// Client-supplied forwarding headers are dropped, and the backend URL must be
// an absolute http(s) URL with a host and no credentials or query.
func Proxy(rawURL string) (http.Handler, error) {
	return newProxy(rawURL, backendTimeout)
}

// newProxy is Proxy with a configurable response-header timeout (for tests).
func newProxy(rawURL string, timeout time.Duration) (http.Handler, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid backend URL") // url.Parse errors embed the whole URL, userinfo included
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("backend URL must be an absolute http(s) URL with a host, without credentials, query or fragment")
	}
	return &httputil.ReverseProxy{
		// Rewrite (unlike Director) drops Forwarded and X-Forwarded-* from the client.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = pr.In.Host // keep the signed Host: emulators read the credential scope and Host
			pr.Out.Header.Del("X-Real-Ip")
			pr.SetXForwarded() // the backend sees the gateway's view of the client, not the client's claim
		},
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: timeout,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
		},
		// Upstream failures become AWS-shaped errors that do not reveal the backend address.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			status, code := http.StatusBadGateway, "BadGateway"
			if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
				status, code = http.StatusGatewayTimeout, "GatewayTimeout"
			}
			awsapi.WriteError(w, awsapi.StyleJSON, status, code, "the backend did not answer")
		},
	}, nil
}

// isTimeout reports whether err is a network timeout.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
