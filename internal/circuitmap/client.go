package circuitmap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

const DefaultEndpoint = "https://api.multiviewer.app/api/v1/circuits"

type Client struct {
	endpoint string
	http     *http.Client
}

func NewClient(endpoint string) (*Client, error) {
	if err := f1id.ValidateHTTPSOrLoopbackEndpoint("endpoint", endpoint); err != nil {
		return nil, err
	}
	return &Client{endpoint: endpoint, http: f1id.NewRestrictedHTTPClient(10 * time.Second)}, nil
}

func (c *Client) Load(ctx context.Context, circuitKey, year int64) (Map, error) {
	if !validID(circuitKey, year) {
		return Map{}, errMap
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%d/%d", c.endpoint, circuitKey, year), nil)
	if err != nil {
		return Map{}, errors.New("invalid circuit map request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "bargeboard/1.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return Map{}, f1id.SanitizedHTTPError(ctx, "fetch circuit map", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Map{}, fmt.Errorf("circuit map HTTP status %d", resp.StatusCode)
	}
	body, err := f1id.ReadBoundedJSONBodyContext(ctx, resp, maxBodyBytes)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, f1id.ErrBodyRead) {
			return Map{}, f1id.SanitizedHTTPError(ctx, "read circuit map", err)
		}
		return Map{}, err
	}
	return Parse(body, circuitKey, year)
}
