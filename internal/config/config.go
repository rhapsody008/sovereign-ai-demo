// Package config parses environment variables into the app configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr string

	ClusterName        string
	ClusterDisplayName string
	ClusterRole        string
	BannerColor        string
	PodName            string
	NodeName           string
	PodNamespace       string

	// Demo-only cluster tile values (no Kubernetes API calls are made).
	FakeNodesTotal int
	FakePodsReady  int
	FakeHPAMax     int

	S3Endpoint           string
	S3UseTLS             bool
	S3InsecureSkipVerify bool
	S3Bucket             string
	S3AccessKey          string
	S3SecretKey          string
	S3Region             string

	NAIBaseURL            string
	NAIAPIKey             string
	NAIModel              string
	NAITimeout            time.Duration
	NAIInsecureSkipVerify bool

	AdminToken  string
	MaxUploadMB int
	SeedSamples bool
}

func Load() (*Config, error) {
	host, _ := os.Hostname()
	c := &Config{
		ListenAddr:   env("LISTEN_ADDR", ":8080"),
		ClusterName:  env("CLUSTER_NAME", "local"),
		ClusterRole:  strings.ToLower(env("CLUSTER_ROLE", "primary")),
		PodName:      env("POD_NAME", host),
		NodeName:     env("NODE_NAME", ""),
		PodNamespace: env("POD_NAMESPACE", ""),

		S3Endpoint:  env("S3_ENDPOINT", ""),
		S3Bucket:    env("S3_BUCKET", "secure-intake"),
		S3AccessKey: env("S3_ACCESS_KEY", ""),
		S3SecretKey: env("S3_SECRET_KEY", ""),
		S3Region:    env("S3_REGION", "us-east-1"),

		NAIBaseURL: env("NAI_BASE_URL", ""),
		NAIAPIKey:  env("NAI_API_KEY", ""),
		NAIModel:   env("NAI_MODEL", ""),
		AdminToken: env("ADMIN_TOKEN", ""),
	}
	c.ClusterDisplayName = env("CLUSTER_DISPLAY_NAME", c.ClusterName)

	var errs []error
	if c.ClusterRole != "primary" && c.ClusterRole != "standby" {
		errs = append(errs, fmt.Errorf("CLUSTER_ROLE must be primary or standby, got %q", c.ClusterRole))
	}
	defColor := "blue"
	if c.ClusterRole == "standby" {
		defColor = "orange"
	}
	c.BannerColor = env("BANNER_COLOR", defColor)

	parseBool := func(key string, def bool) bool {
		v, err := strconv.ParseBool(env(key, strconv.FormatBool(def)))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return v
	}
	parseInt := func(key string, def int) int {
		v, err := strconv.Atoi(env(key, strconv.Itoa(def)))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return v
	}
	c.S3UseTLS = parseBool("S3_USE_TLS", true)
	c.S3InsecureSkipVerify = parseBool("S3_INSECURE_SKIP_VERIFY", false)
	c.NAIInsecureSkipVerify = parseBool("NAI_INSECURE_SKIP_VERIFY", false)
	c.SeedSamples = parseBool("SEED_SAMPLE_RECORDS", true)
	c.MaxUploadMB = parseInt("MAX_UPLOAD_MB", 8)
	c.FakeNodesTotal = parseInt("DEMO_NODES", 3)
	c.FakePodsReady = parseInt("DEMO_PODS", 2)
	c.FakeHPAMax = parseInt("DEMO_HPA_MAX", 8)

	d, err := time.ParseDuration(env("NAI_TIMEOUT", "60s"))
	if err != nil {
		errs = append(errs, fmt.Errorf("NAI_TIMEOUT: %w", err))
	}
	c.NAITimeout = d

	// S3_ENDPOINT empty → in-memory store (local dev only; failover won't work).
	if c.S3Endpoint != "" && (c.S3AccessKey == "" || c.S3SecretKey == "") {
		errs = append(errs, errors.New("S3_ACCESS_KEY and S3_SECRET_KEY are required when S3_ENDPOINT is set"))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
