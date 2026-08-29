package f1livetimingreceiver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	affinityCookieName = "AWSALBCORS"
	maxTokenFileSize   = 64 * 1024
)

type connectionCredentials struct {
	token          string
	affinityCookie *http.Cookie
}

func (connectionCredentials) String() string {
	return "connection credentials [REDACTED]"
}

func (connectionCredentials) GoString() string {
	return "connection credentials [REDACTED]"
}

func (c connectionCredentials) headers() http.Header {
	return c.headersWithCookies([]*http.Cookie{c.affinityCookie})
}

func (c connectionCredentials) headersWithCookies(cookies []*http.Cookie) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+c.token)

	pairs := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" || cookie.Value == "" {
			continue
		}
		pair := (&http.Cookie{Name: cookie.Name, Value: cookie.Value}).String()
		if pair != "" {
			pairs = append(pairs, pair)
		}
	}
	if len(pairs) > 0 {
		headers.Set("Cookie", strings.Join(pairs, "; "))
	}
	return headers
}

func bootstrapConnection(ctx context.Context, client *http.Client, cfg *Config) (connectionCredentials, error) {
	token, err := readTokenFile(cfg.Auth.TokenFile)
	if err != nil {
		return connectionCredentials{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodOptions, cfg.NegotiateEndpoint, nil)
	if err != nil {
		return connectionCredentials{}, fmt.Errorf("create negotiation preflight: %w", err)
	}

	response, err := client.Do(request)
	if err != nil {
		return connectionCredentials{}, fmt.Errorf("perform negotiation preflight: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)

	return credentialsFromPreflight(token, response.StatusCode, response.Status, response.Cookies())
}

func readTokenFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open auth token file: %w", err)
	}
	defer file.Close()

	contents, err := io.ReadAll(io.LimitReader(file, maxTokenFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read auth token file: %w", err)
	}
	return parseToken(contents)
}

func parseToken(contents []byte) (string, error) {
	if len(contents) > maxTokenFileSize {
		return "", fmt.Errorf("auth token file exceeds %d bytes", maxTokenFileSize)
	}

	token := strings.TrimSpace(string(contents))
	if token == "" {
		return "", fmt.Errorf("auth token file is empty")
	}
	return token, nil
}

func credentialsFromPreflight(
	token string,
	statusCode int,
	status string,
	cookies []*http.Cookie,
) (connectionCredentials, error) {
	for _, cookie := range cookies {
		if cookie != nil && cookie.Name == affinityCookieName && cookie.Value != "" {
			affinityCookie := *cookie
			return connectionCredentials{token: token, affinityCookie: &affinityCookie}, nil
		}
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return connectionCredentials{}, fmt.Errorf("negotiation preflight returned %s without %s cookie", status, affinityCookieName)
	}
	return connectionCredentials{}, fmt.Errorf("negotiation preflight did not return %s cookie", affinityCookieName)
}
