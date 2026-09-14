package httptransport

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

const maxIntrospectionBytes = 256 * 1024

func TokenVerifier(profile Profile) (auth.TokenVerifier, error) {
	if profile.Auth.Mode == "local_token" {
		file, err := os.Open(profile.Auth.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("read local token: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 4097))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.New("read local token")
		}
		token := strings.TrimSpace(string(data))
		if len(token) < 32 || len(token) > 4096 {
			return nil, errors.New("local token length must be 32..4096 bytes")
		}
		return func(_ context.Context, candidate string, _ *http.Request) (*auth.TokenInfo, error) {
			if len(candidate) != len(token) || subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) != 1 {
				return nil, auth.ErrInvalidToken
			}
			return &auth.TokenInfo{Scopes: append([]string(nil), profile.Auth.Scopes...), Expiration: time.Now().Add(24 * time.Hour), UserID: "local"}, nil
		}, nil
	}
	clientID, okID := os.LookupEnv(profile.Auth.ClientIDEnv)
	secret, okSecret := os.LookupEnv(profile.Auth.ClientSecretEnv)
	if !okID || !okSecret || clientID == "" || secret == "" {
		return nil, errors.New("OAuth introspection credentials are unavailable")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConns: 16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second}}
	return introspectionVerifier(profile, client, clientID, secret), nil
}

func introspectionVerifier(profile Profile, client *http.Client, clientID, secret string) auth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, profile.Auth.IntrospectionURL, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, errors.New("create introspection request")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(clientID, secret)
		response, err := client.Do(req)
		if err != nil {
			return nil, errors.New("OAuth introspection unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			return nil, errors.New("OAuth introspection unavailable")
		}
		if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
			return nil, errors.New("OAuth introspection response invalid")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxIntrospectionBytes+1))
		if err != nil || len(body) > maxIntrospectionBytes {
			return nil, errors.New("OAuth introspection response invalid")
		}
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.UseNumber()
		var result struct {
			Active   bool        `json:"active"`
			Exp      json.Number `json:"exp"`
			Scope    string      `json:"scope"`
			Sub      string      `json:"sub"`
			Aud      any         `json:"aud"`
			Resource string      `json:"resource"`
		}
		if err := decoder.Decode(&result); err != nil {
			return nil, errors.New("OAuth introspection response invalid")
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, errors.New("OAuth introspection response invalid")
		}
		if !result.Active {
			return nil, auth.ErrInvalidToken
		}
		exp, err := strconv.ParseInt(result.Exp.String(), 10, 64)
		if err != nil || exp <= 0 {
			return nil, auth.ErrInvalidToken
		}
		if !audienceMatches(result.Aud, result.Resource, profile.Auth.Audience) {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{Scopes: strings.Fields(result.Scope), Expiration: time.Unix(exp, 0), UserID: result.Sub}, nil
	}
}

func audienceMatches(aud any, resource, expected string) bool {
	if resource == expected {
		return true
	}
	switch value := aud.(type) {
	case string:
		return value == expected
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok && text == expected {
				return true
			}
		}
	}
	return false
}
