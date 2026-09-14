// Package httptransport provides the opt-in authenticated Streamable HTTP host.
package httptransport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const maxProfileBytes = 256 * 1024

type Profile struct {
	Listen         string        `yaml:"listen"`
	Endpoint       string        `yaml:"endpoint"`
	ResourceURI    string        `yaml:"resource_uri"`
	AllowedOrigins []string      `yaml:"allowed_origins"`
	AllowedHosts   []string      `yaml:"allowed_hosts"`
	TLS            TLSProfile    `yaml:"tls"`
	Auth           AuthProfile   `yaml:"auth"`
	Limits         LimitsProfile `yaml:"limits"`
	Audit          AuditProfile  `yaml:"audit"`
}

type TLSProfile struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type AuthProfile struct {
	Mode                 string   `yaml:"mode"`
	TokenFile            string   `yaml:"token_file"`
	Scopes               []string `yaml:"scopes"`
	IntrospectionURL     string   `yaml:"introspection_url"`
	ClientIDEnv          string   `yaml:"client_id_env"`
	ClientSecretEnv      string   `yaml:"client_secret_env"`
	AuthorizationServers []string `yaml:"authorization_servers"`
	Audience             string   `yaml:"audience"`
}

type LimitsProfile struct {
	BodyBytes          int64 `yaml:"body_bytes"`
	ConcurrentRequests int   `yaml:"concurrent_requests"`
	PerPrincipal       int   `yaml:"per_principal"`
	RequestsPerMinute  int   `yaml:"requests_per_minute"`
	ReadHeaderSeconds  int   `yaml:"read_header_seconds"`
	ReadSeconds        int   `yaml:"read_seconds"`
	IdleSeconds        int   `yaml:"idle_seconds"`
	ShutdownSeconds    int   `yaml:"shutdown_seconds"`
}

type AuditProfile struct {
	RetentionDays int `yaml:"retention_days"`
}

func LoadProfile(path string) (Profile, error) {
	file, err := os.Open(path)
	if err != nil {
		return Profile{}, fmt.Errorf("open HTTP profile: %w", err)
	}
	defer file.Close()
	limited := &limitedReader{reader: file, remaining: maxProfileBytes + 1}
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(limited); err != nil {
		return Profile{}, fmt.Errorf("read HTTP profile: %w", err)
	}
	if raw.Len() > maxProfileBytes {
		return Profile{}, errors.New("HTTP profile exceeds size limit")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw.Bytes()))
	decoder.KnownFields(true)
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("decode HTTP profile: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Profile{}, errors.New("HTTP profile must contain exactly one YAML document")
	}
	if err := profile.normalize(filepath.Dir(path)); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func (p *Profile) normalize(profileDir string) error {
	if p.Listen == "" {
		p.Listen = "127.0.0.1:8080"
	}
	if p.Endpoint == "" {
		p.Endpoint = "/mcp"
	}
	if !strings.HasPrefix(p.Endpoint, "/") || strings.Contains(p.Endpoint, "?") || p.Endpoint == "/" {
		return errors.New("HTTP endpoint must be an absolute non-root path")
	}
	resource, err := url.Parse(p.ResourceURI)
	if err != nil || resource.Scheme == "" || resource.Host == "" || resource.Fragment != "" {
		return errors.New("resource_uri must be an absolute HTTP(S) URI")
	}
	host, _, err := net.SplitHostPort(p.Listen)
	if err != nil {
		return errors.New("listen must include host and port")
	}
	loopback := strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	directTLS := p.TLS.CertFile != "" && p.TLS.KeyFile != ""
	if (p.TLS.CertFile == "") != (p.TLS.KeyFile == "") {
		return errors.New("both TLS cert_file and key_file are required")
	}
	if !loopback && !directTLS {
		return errors.New("non-loopback HTTP listen requires direct TLS")
	}
	if directTLS {
		if !filepath.IsAbs(p.TLS.CertFile) {
			p.TLS.CertFile = filepath.Join(profileDir, p.TLS.CertFile)
		}
		if !filepath.IsAbs(p.TLS.KeyFile) {
			p.TLS.KeyFile = filepath.Join(profileDir, p.TLS.KeyFile)
		}
		if resource.Scheme != "https" {
			return errors.New("direct TLS requires an https resource_uri")
		}
	}
	if p.Auth.Mode != "local_token" && p.Auth.Mode != "oauth_introspection" {
		return errors.New("auth.mode must be local_token or oauth_introspection")
	}
	if p.Auth.Mode == "local_token" {
		if !loopback {
			return errors.New("local_token auth is limited to loopback")
		}
		if p.Auth.TokenFile == "" {
			return errors.New("local_token auth requires token_file")
		}
		if !filepath.IsAbs(p.Auth.TokenFile) {
			p.Auth.TokenFile = filepath.Join(profileDir, p.Auth.TokenFile)
		}
		if len(p.Auth.Scopes) == 0 {
			return errors.New("local_token auth requires scopes")
		}
	} else {
		if resource.Scheme != "https" {
			return errors.New("OAuth mode requires an https resource_uri")
		}
		endpoint, err := url.Parse(p.Auth.IntrospectionURL)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
			return errors.New("OAuth introspection_url must be HTTPS")
		}
		if p.Auth.Audience == "" {
			p.Auth.Audience = p.ResourceURI
		}
		if len(p.Auth.AuthorizationServers) == 0 {
			return errors.New("OAuth auth requires authorization_servers")
		}
		for _, issuer := range p.Auth.AuthorizationServers {
			parsed, err := url.Parse(issuer)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				return errors.New("authorization_servers must contain HTTPS issuer URIs")
			}
		}
		if p.Auth.ClientIDEnv == "" || p.Auth.ClientSecretEnv == "" {
			return errors.New("OAuth auth requires credential environment variable names")
		}
	}
	if len(p.AllowedHosts) == 0 {
		p.AllowedHosts = []string{resource.Host}
	}
	if len(p.AllowedHosts) > 64 || len(p.AllowedOrigins) > 64 {
		return errors.New("HTTP allowlist exceeds supported bounds")
	}
	for _, allowed := range p.AllowedHosts {
		if allowed == "" || strings.ContainsAny(allowed, "/\\") {
			return errors.New("allowed_hosts contains an invalid host")
		}
	}
	if p.Limits.BodyBytes == 0 {
		p.Limits.BodyBytes = 1 << 20
	}
	if p.Limits.ConcurrentRequests == 0 {
		p.Limits.ConcurrentRequests = 16
	}
	if p.Limits.PerPrincipal == 0 {
		p.Limits.PerPrincipal = 4
	}
	if p.Limits.RequestsPerMinute == 0 {
		p.Limits.RequestsPerMinute = 120
	}
	if p.Limits.ReadHeaderSeconds == 0 {
		p.Limits.ReadHeaderSeconds = 10
	}
	if p.Limits.ReadSeconds == 0 {
		p.Limits.ReadSeconds = 30
	}
	if p.Limits.IdleSeconds == 0 {
		p.Limits.IdleSeconds = 60
	}
	if p.Limits.ShutdownSeconds == 0 {
		p.Limits.ShutdownSeconds = 30
	}
	if p.Audit.RetentionDays == 0 {
		p.Audit.RetentionDays = 30
	}
	if p.Limits.BodyBytes < 1024 || p.Limits.BodyBytes > 16<<20 || p.Limits.ConcurrentRequests < 1 || p.Limits.ConcurrentRequests > 1024 || p.Limits.PerPrincipal < 1 || p.Limits.PerPrincipal > p.Limits.ConcurrentRequests || p.Limits.RequestsPerMinute < 1 || p.Limits.RequestsPerMinute > 100000 || p.Limits.ReadHeaderSeconds < 1 || p.Limits.ReadHeaderSeconds > 300 || p.Limits.ReadSeconds < 1 || p.Limits.ReadSeconds > 3600 || p.Limits.IdleSeconds < 1 || p.Limits.IdleSeconds > 3600 || p.Limits.ShutdownSeconds < 1 || p.Limits.ShutdownSeconds > 300 || p.Audit.RetentionDays < 1 || p.Audit.RetentionDays > 3650 {
		return errors.New("HTTP limits exceed supported bounds")
	}
	return nil
}

func (p Profile) ReadHeaderTimeout() time.Duration {
	return time.Duration(p.Limits.ReadHeaderSeconds) * time.Second
}
func (p Profile) ReadTimeout() time.Duration {
	return time.Duration(p.Limits.ReadSeconds) * time.Second
}
func (p Profile) IdleTimeout() time.Duration {
	return time.Duration(p.Limits.IdleSeconds) * time.Second
}
func (p Profile) ShutdownTimeout() time.Duration {
	return time.Duration(p.Limits.ShutdownSeconds) * time.Second
}

type limitedReader struct {
	reader    *os.File
	remaining int64
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("size limit reached")
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}
