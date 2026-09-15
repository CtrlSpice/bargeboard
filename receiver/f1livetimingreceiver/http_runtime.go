package f1livetimingreceiver

import (
	"context"
	"net/http"
	"time"
)

// coder/websocket v1.8.15 otherwise reads rejected bodies for diagnostics before
// closing them. Intercept non-101 responses before Dial can read irrelevant data
// or http.Client can follow a redirect. Successful upgrades retain the original
// writable body and the codec's handshake validation/cleanup ownership.
type upgradeHTTPTransport struct{ base http.RoundTripper }

func (t upgradeHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
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
	if response.StatusCode != http.StatusSwitchingProtocols {
		err := setupHTTPFailure(stageUpgrade, response.StatusCode, response.Header.Get("Retry-After"), now)
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	return response, nil
}

func upgradeHTTPClient(client *http.Client) *http.Client {
	clone := *client
	transport := clone.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = upgradeHTTPTransport{base: transport}
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
