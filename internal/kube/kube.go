// Package kube is a minimal in-cluster Kubernetes API client built on the
// standard library only. It reads the service-account token on every request,
// because projected tokens are rotated by the kubelet.
package kube

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	// serviceAccountDir is where Kubernetes projects the service account.
	serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
	// caFile is the cluster CA bundle used to verify the API server.
	caFile = serviceAccountDir + "/ca.crt"
	// tokenFile holds the bearer token for the service account.
	tokenFile = serviceAccountDir + "/token"
)

// Client talks to the Kubernetes API server. The fields are exported so tests
// can point it at an httptest server with a temporary token file.
type Client struct {
	BaseURL   string
	TokenFile string
	HTTP      *http.Client
}

// InCluster builds a client for the API server of the cluster the pod runs in.
// It requires KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT and a valid
// cluster CA bundle.
func InCluster() (*Client, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not running in a cluster: KUBERNETES_SERVICE_HOST/PORT are not set")
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse cluster CA %s: no certificates found", caFile)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &Client{
		BaseURL:   "https://" + net.JoinHostPort(host, port),
		TokenFile: tokenFile,
		HTTP:      &http.Client{Transport: tr},
	}, nil
}

// CountReadyPods counts the pods in namespace that match labelSelector and are
// Running, Ready and not being deleted.
func (c *Client) CountReadyPods(ctx context.Context, namespace, labelSelector string) (int, error) {
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return 0, fmt.Errorf("read service-account token: %w", err)
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
	if labelSelector != "" {
		q := url.Values{}
		q.Set("labelSelector", labelSelector)
		endpoint += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Kubernetes API unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, fmt.Errorf("read Kubernetes API response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Kubernetes API returned HTTP %d: %s", resp.StatusCode, short(data))
	}
	var list struct {
		Items []struct {
			Metadata struct {
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return 0, fmt.Errorf("unexpected pod list response: %w", err)
	}
	ready := 0
	for _, p := range list.Items {
		if p.Metadata.DeletionTimestamp != nil || p.Status.Phase != "Running" {
			continue
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				ready++
				break
			}
		}
	}
	return ready, nil
}

// short extracts a readable message from an error response body.
func short(b []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		return e.Message
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}
