package f1livetimingreceiver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapConnection(t *testing.T) {
	const token = "secret-subscription-token"

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodOptions {
			t.Errorf("request method = %s, want OPTIONS", request.Method)
		}
		if got := request.Header.Get("Authorization"); got != "" {
			t.Errorf("preflight Authorization header = %q, want empty", got)
		}
		http.SetCookie(writer, &http.Cookie{Name: affinityCookieName, Value: "affinity-token"})
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}))
	t.Cleanup(server.Close)

	cfg := createDefaultConfig().(*Config)
	cfg.Auth.TokenFile = writeTokenFile(t, token+"\n")
	cfg.NegotiateEndpoint = server.URL

	credentials, err := bootstrapConnection(context.Background(), server.Client(), cfg)
	if err != nil {
		t.Fatalf("bootstrapConnection() error = %v", err)
	}
	if got := credentials.headers().Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization header = %q", got)
	}
	if got := credentials.headers().Get("Cookie"); got != affinityCookieName+"=affinity-token" {
		t.Errorf("Cookie header = %q", got)
	}
}

func TestBootstrapConnectionRejectsInvalidPreflight(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name: "failed response",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusUnauthorized)
			},
			wantErr: "401 Unauthorized",
		},
		{
			name: "missing affinity cookie",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			},
			wantErr: affinityCookieName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			t.Cleanup(server.Close)

			cfg := createDefaultConfig().(*Config)
			cfg.Auth.TokenFile = writeTokenFile(t, "subscription-token")
			cfg.NegotiateEndpoint = server.URL

			_, err := bootstrapConnection(context.Background(), server.Client(), cfg)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("bootstrapConnection() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestParseToken(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
		wantErr  string
	}{
		{name: "raw token", contents: "subscription-token", want: "subscription-token"},
		{name: "surrounding whitespace", contents: " \nsubscription-token\r\n", want: "subscription-token"},
		{name: "empty", contents: " \n", wantErr: "empty"},
		{name: "too large", contents: strings.Repeat("x", maxTokenFileSize+1), wantErr: "exceeds"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseToken([]byte(test.contents))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseToken() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseToken() error = %v", err)
			}
			if got != test.want {
				t.Errorf("parseToken() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCredentialsFromPreflight(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		status     string
		cookies    []*http.Cookie
		wantErr    string
	}{
		{
			name:       "successful response with affinity cookie",
			statusCode: http.StatusNoContent,
			status:     "204 No Content",
			cookies:    []*http.Cookie{{Name: affinityCookieName, Value: "affinity-token"}},
		},
		{
			name:       "failed response with affinity cookie",
			statusCode: http.StatusMethodNotAllowed,
			status:     "405 Method Not Allowed",
			cookies:    []*http.Cookie{{Name: affinityCookieName, Value: "affinity-token"}},
		},
		{
			name:       "successful response without affinity cookie",
			statusCode: http.StatusNoContent,
			status:     "204 No Content",
			cookies:    []*http.Cookie{{Name: "other", Value: "cookie"}},
			wantErr:    affinityCookieName,
		},
		{
			name:       "failed response without affinity cookie",
			statusCode: http.StatusMethodNotAllowed,
			status:     "405 Method Not Allowed",
			wantErr:    "405 Method Not Allowed",
		},
		{
			name:       "empty affinity cookie",
			statusCode: http.StatusNoContent,
			status:     "204 No Content",
			cookies:    []*http.Cookie{{Name: affinityCookieName}},
			wantErr:    affinityCookieName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials, err := credentialsFromPreflight(
				"subscription-token",
				test.statusCode,
				test.status,
				test.cookies,
			)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("credentialsFromPreflight() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("credentialsFromPreflight() error = %v", err)
			}
			if got := credentials.headers().Get("Cookie"); got != affinityCookieName+"=affinity-token" {
				t.Errorf("Cookie header = %q", got)
			}
		})
	}
}

func TestConnectionCredentialsAreRedacted(t *testing.T) {
	credentials := connectionCredentials{
		token:          "secret-subscription-token",
		affinityCookie: &http.Cookie{Name: affinityCookieName, Value: "secret-affinity-token"},
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		got := fmt.Sprintf(format, credentials)
		if strings.Contains(got, "secret") {
			t.Errorf("Sprintf(%q) exposed credentials: %q", format, got)
		}
	}
}

func TestConnectionCredentialsHeadersReturnsFreshValue(t *testing.T) {
	credentials := connectionCredentials{
		token:          "subscription-token",
		affinityCookie: &http.Cookie{Name: affinityCookieName, Value: "affinity-token"},
	}

	first := credentials.headers()
	first.Set("Authorization", "changed")
	if got := credentials.headers().Get("Authorization"); got != "Bearer subscription-token" {
		t.Errorf("Authorization header = %q", got)
	}
}

func TestCredentialsFromPreflightCopiesAffinityCookie(t *testing.T) {
	cookie := &http.Cookie{Name: affinityCookieName, Value: "affinity-token"}
	credentials, err := credentialsFromPreflight(
		"subscription-token",
		http.StatusNoContent,
		"204 No Content",
		[]*http.Cookie{cookie},
	)
	if err != nil {
		t.Fatalf("credentialsFromPreflight() error = %v", err)
	}

	cookie.Value = "changed"
	if got := credentials.headers().Get("Cookie"); got != affinityCookieName+"=affinity-token" {
		t.Errorf("Cookie header after input mutation = %q", got)
	}
}

func writeTokenFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f1tv-token")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
