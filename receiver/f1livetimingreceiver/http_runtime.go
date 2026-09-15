package f1livetimingreceiver

import (
	"context"
	"net/http"
	"time"
)

// Classify before http.Client parses Location (which precedes CheckRedirect) or
// coder/websocket v1.8.15 reads rejected bodies for diagnostics. Accepted bodies
// retain their existing setup/codec ownership, including writable 101 transports.
type setupHTTPTransport struct {
	base  http.RoundTripper
	stage setupStage
}

func (t setupHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || response == nil {
		return response, err
	}
	now := time.Now()
	if err := setupContextError(request.Context(), now); err != nil {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	if t.stage == stagePreflight {
		if _, err := credentialsFromPreflight("", response.StatusCode, response.Cookies()); err == nil {
			if response.StatusCode >= 300 && response.StatusCode < 400 {
				// Cookie acceptance is authoritative. Location has no authority to
				// redirect or invalidate it; hide it only from http.Client, retaining
				// the original status, cookies, body, and caller-owned headers.
				clone := *response
				clone.Header = response.Header.Clone()
				clone.Header.Del("Location")
				return &clone, nil
			}
			return response, nil
		}
	}
	if classifySetupHTTP(t.stage, response.StatusCode) != httpAccept {
		err := setupHTTPFailure(t.stage, response.StatusCode, response.Header.Get("Retry-After"), now)
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	return response, nil
}

func setupHTTPClient(client *http.Client, stage setupStage) *http.Client {
	clone := *client
	transport := clone.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = setupHTTPTransport{base: transport, stage: stage}
	return &clone
}

// An elapsed deadline wins even if its cancellation callback has not run yet.
func setupContextError(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !now.Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}
