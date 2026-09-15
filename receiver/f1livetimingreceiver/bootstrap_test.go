package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
			wantErr: "HTTP 401",
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
			if test.name == "failed response" {
				if failure := asSetupHTTPError(err); failure == nil || *failure != (setupHTTPError{stage: stagePreflight, status: 401}) {
					t.Errorf("bootstrapConnection() HTTP failure = %#v", failure)
				}
			} else if !errors.Is(err, errInvalidLiveTimingData) {
				t.Errorf("bootstrapConnection() error does not wrap errInvalidLiveTimingData")
			}
		})
	}
}

func TestBootstrapConnectionUsesOnlyPreflightHeaders(t *testing.T) {
	for _, test := range []struct {
		name       string
		cookie     bool
		contextErr error
		wantErr    string
	}{
		{name: "cookie on 405", cookie: true},
		{name: "missing cookie", wantErr: "negotiation preflight returned HTTP 405"},
		{name: "canceled with cookie", cookie: true, contextErr: context.Canceled, wantErr: "perform negotiation preflight: context canceled"},
		{name: "canceled without cookie", contextErr: context.Canceled, wantErr: "perform negotiation preflight: context canceled"},
		{name: "deadline with cookie", cookie: true, contextErr: context.DeadlineExceeded, wantErr: "perform negotiation preflight: context deadline exceeded"},
		{name: "deadline without cookie", contextErr: context.DeadlineExceeded, wantErr: "perform negotiation preflight: context deadline exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			if test.contextErr == context.DeadlineExceeded {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadlineCause(ctx, time.Unix(1, 0), errors.New("synthetic-confidential-cause"))
				defer stop()
			}
			body := &preflightTestBody{}
			client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				if test.contextErr == context.Canceled {
					cancel(errors.New("synthetic-confidential-cause"))
				}
				header := make(http.Header)
				if test.cookie {
					header.Set("Set-Cookie", "AWSALBCORS=affinity-token")
				}
				return &http.Response{StatusCode: http.StatusMethodNotAllowed, Header: header, Body: body}, nil
			})}
			cfg := connectionTestConfig(t, "http://example.test")
			got, err := bootstrapConnection(ctx, client, cfg)
			want := connectionCredentials{}
			if test.wantErr == "" {
				want = connectionCredentials{token: "subscription-token", affinityCookie: &http.Cookie{
					Name: affinityCookieName, Value: "affinity-token", Raw: "AWSALBCORS=affinity-token",
				}}
				if err != nil {
					t.Errorf("bootstrapConnection() error = %v", err)
				}
			} else {
				if err == nil || err.Error() != test.wantErr {
					t.Errorf("bootstrapConnection() error = %v, want %q", err, test.wantErr)
				}
				wantClass := test.contextErr
				if wantClass == nil {
					if failure := asSetupHTTPError(err); failure == nil || *failure != (setupHTTPError{stage: stagePreflight, status: 405}) {
						t.Errorf("HTTP failure = %#v", failure)
					}
				} else if !errors.Is(err, wantClass) {
					t.Errorf("bootstrapConnection() error = %v, want classification %v", err, wantClass)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("bootstrapConnection() credentials = %#v, want %#v", got, want)
			}
			if *body != (preflightTestBody{closes: 1}) {
				t.Errorf("preflight body operations = %+v, want zero reads and one close", *body)
			}
		})
	}
}

func TestBootstrapConnectionDoesNotWaitForPreflightBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.SetCookie(writer, &http.Cookie{Name: affinityCookieName, Value: "affinity-token"})
		writer.WriteHeader(http.StatusMethodNotAllowed)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := bootstrapConnection(ctx, server.Client(), connectionTestConfig(t, server.URL))
	want := connectionCredentials{token: "subscription-token", affinityCookie: &http.Cookie{
		Name: affinityCookieName, Value: "affinity-token", Raw: "AWSALBCORS=affinity-token",
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("bootstrapConnection() = (%#v, %v), want (%#v, nil)", got, err, want)
	}
	if err := ctx.Err(); err != nil {
		t.Errorf("bootstrapConnection() waited for the stalled body until %v", err)
	}
}

type preflightTestBody struct {
	reads  int
	closes int
}

func (body *preflightTestBody) Read([]byte) (int, error) {
	body.reads++
	return 0, errors.New("synthetic-confidential-body-error")
}

func (body *preflightTestBody) Close() error {
	body.closes++
	return errors.New("synthetic-confidential-close-error")
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
		cookies    []*http.Cookie
		wantErr    string
	}{
		{
			name:       "successful response with affinity cookie",
			statusCode: http.StatusNoContent,
			cookies:    []*http.Cookie{{Name: affinityCookieName, Value: "affinity-token"}},
		},
		{
			name:       "failed response with affinity cookie",
			statusCode: http.StatusMethodNotAllowed,
			cookies:    []*http.Cookie{{Name: affinityCookieName, Value: "affinity-token"}},
		},
		{
			name:       "successful response without affinity cookie",
			statusCode: http.StatusNoContent,
			cookies:    []*http.Cookie{{Name: "other", Value: "cookie"}},
			wantErr:    affinityCookieName,
		},
		{
			name:       "failed response without affinity cookie",
			statusCode: http.StatusMethodNotAllowed,
			wantErr:    "HTTP 405",
		},
		{
			name:       "empty affinity cookie",
			statusCode: http.StatusNoContent,
			cookies:    []*http.Cookie{{Name: affinityCookieName}},
			wantErr:    affinityCookieName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials, err := credentialsFromPreflight(
				"subscription-token",
				test.statusCode,
				test.cookies,
			)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("credentialsFromPreflight() error = %v, want containing %q", err, test.wantErr)
				}
				if test.statusCode == http.StatusMethodNotAllowed {
					if failure := asSetupHTTPError(err); failure == nil || *failure != (setupHTTPError{stage: stagePreflight, status: 405}) {
						t.Errorf("HTTP failure = %#v", failure)
					}
				} else if !errors.Is(err, errInvalidLiveTimingData) {
					t.Errorf("credentialsFromPreflight() error does not wrap errInvalidLiveTimingData")
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
