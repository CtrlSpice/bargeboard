package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestHTTPSetupResponseBoundary(t *testing.T) {
	for _, stage := range []setupStage{stagePreflight, stageNegotiate, stageUpgrade} {
		for _, status := range []int{300, 301, 302, 303, 307, 308, 400, 401, 403, 404, 405, 408, 429, 500, 501, 502, 503, 504, 505} {
			t.Run(fmt.Sprintf("%s/%d", stage, status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					body := &preflightTestBody{}
					var methods []string
					client := livenessWebSocketClient(t, func(*websocket.Conn) { t.Error("unexpected upgrade") })
					base := client.Transport
					client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
						methods = append(methods, request.Method)
						method := map[setupStage]string{stagePreflight: http.MethodOptions, stageNegotiate: http.MethodPost, stageUpgrade: http.MethodGet}[stage]
						if request.Method != method {
							return base.RoundTrip(request)
						}
						return &http.Response{StatusCode: status, Header: http.Header{
							"Retry-After": {"60"}, "Location": {"https://private-redirect.test/private-token"}, "X-Private": {"private-header"},
						}, Body: body}, nil
					})
					client.CheckRedirect = func(*http.Request, []*http.Request) error {
						t.Error("redirect attempted")
						return http.ErrUseLastResponse
					}
					// Production preflight and negotiate use the receiver's non-following
					// client policy; upgrade rejects before http.Client's redirect hook.
					if stage != stageUpgrade {
						client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
					}
					now := time.Now()
					connection, err := connectSignalR(t.Context(), client, connectionTestConfig(t, "http://synthetic.test/private-route"))
					want := setupHTTPError{stage: stage, status: status}
					if classifySetupHTTP(stage, status) == httpRetry {
						want.retryAt = now.Add(time.Minute)
					}
					failure := asSetupHTTPError(err)
					if connection != nil || failure == nil || *failure != want || errors.Is(err, errInvalidLiveTimingData) || errors.Unwrap(err) != nil {
						t.Fatalf("connection=%v error=%#v; want %#v", connection, err, want)
					}
					if *body != (preflightTestBody{closes: 1}) {
						t.Fatalf("body = %+v", body)
					}
					wantMethods := []string{http.MethodOptions, http.MethodPost, http.MethodGet}[:int(stage)]
					if !reflect.DeepEqual(methods, wantMethods) {
						t.Fatalf("methods = %v, want %v", methods, wantMethods)
					}
					for _, format := range []string{"%v", "%+v", "%#v"} {
						if got := fmt.Sprintf(format, err); got != fmt.Sprintf("%s returned HTTP %d", stage, status) {
							t.Fatalf("error format = %q", got)
						}
					}
				})
			})
		}
	}
}

func TestHTTPSetupCancellationAtHeaders(t *testing.T) {
	for _, stage := range []setupStage{stagePreflight, stageNegotiate, stageUpgrade} {
		for _, status := range []int{101, 200, 401, 503} {
			for _, deadline := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/deadline=%v", stage, status, deadline), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						ctx, cancel := context.WithCancelCause(t.Context())
						defer cancel(nil)
						wantErr := context.Canceled
						if deadline {
							var stop context.CancelFunc
							ctx, stop = context.WithTimeoutCause(ctx, time.Second, errors.New("private-cause"))
							defer stop()
							wantErr = context.DeadlineExceeded
						}
						body := &preflightTestBody{}
						client := livenessWebSocketClient(t, func(*websocket.Conn) { t.Error("unexpected upgrade") })
						base := client.Transport
						calls := 0
						client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
							calls++
							if calls != int(stage) {
								return base.RoundTrip(request)
							}
							if deadline {
								time.Sleep(time.Second)
							} else {
								cancel(errors.New("private-cause"))
							}
							return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"999999"}, "Set-Cookie": {"AWSALBCORS=private-cookie"}}, Body: body}, nil
						})
						connection, err := connectSignalR(ctx, client, connectionTestConfig(t, "http://synthetic.test/private-route"))
						if connection != nil || !errors.Is(err, wantErr) || errors.Unwrap(err) != wantErr || asSetupHTTPError(err) != nil || errors.Is(err, errInvalidLiveTimingData) {
							t.Fatalf("connection=%v error=%v", connection, err)
						}
						if *body != (preflightTestBody{closes: 1}) || calls != int(stage) {
							t.Fatalf("body=%+v calls=%d", body, calls)
						}
						if strings.Contains(fmt.Sprintf("%+v %#v", err, err), "private") {
							t.Fatal("private cancellation escaped")
						}
					})
				})
			}
		}
	}
}

func TestHTTPUpgrade101CodecFailureIsProtocol(t *testing.T) {
	client := livenessWebSocketClient(t, func(*websocket.Conn) { t.Error("unexpected upgrade") })
	base := client.Transport
	body := &preflightTestBody{}
	client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			return base.RoundTrip(request)
		}
		return &http.Response{StatusCode: 101, Header: http.Header{"Connection": {"private-invalid-handshake"}, "Retry-After": {"999999"}}, Body: body}, nil
	})
	connection, err := connectSignalR(t.Context(), client, connectionTestConfig(t, "http://synthetic.test/private-route"))
	if connection != nil || !errors.Is(err, errInvalidLiveTimingData) || asSetupHTTPError(err) != nil || err.Error() != "invalid F1 live timing data: SignalR WebSocket upgrade response is invalid" {
		t.Fatalf("connection=%v error=%v", connection, err)
	}
	// The pinned codec owns 101 validation and its failed-handshake cleanup.
	if *body != (preflightTestBody{reads: 1, closes: 1}) {
		t.Fatalf("codec cleanup = %+v", body)
	}
}

func TestHTTPAcceptedNegotiationRemainsBoundedAndValidated(t *testing.T) {
	for _, contents := range []string{"{", strings.Repeat(" ", maxNegotiateResponseSize+1)} {
		body := &countedHTTPBody{Reader: strings.NewReader(contents)}
		client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Retry-After": {"60"}}, Body: body}, nil
		})}
		got, cookies, err := negotiate(t.Context(), client, "http://synthetic.test", connectionCredentials{}, nil)
		if got != (negotiation{}) || cookies != nil || !errors.Is(err, errInvalidLiveTimingData) || asSetupHTTPError(err) != nil || body.closes != 1 || body.bytes > maxNegotiateResponseSize+1 {
			t.Fatalf("negotiation=%#v cookies=%v error=%v body=%+v", got, cookies, err, body)
		}
	}
}

type countedHTTPBody struct {
	*strings.Reader
	bytes, closes int
}

func (b *countedHTTPBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.bytes += n
	return n, err
}
func (b *countedHTTPBody) Close() error { b.closes++; return nil }

func TestHTTPInitialStartFailsWithoutRetryAndCleansUp(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				core, logs := observer.New(zap.InfoLevel)
				settings := receivertest.NewNopSettings(Type)
				settings.Logger = zap.New(core)
				r := newLiveTimingReceiver(connectionTestConfig(t, "http://synthetic.test/private-route"), settings)
				calls := 0
				body := &preflightTestBody{}
				r.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"60"}}, Body: body}, nil
				})
				host := &statusHost{events: make(chan *componentstatus.Event, 8)}
				started := time.Now()
				err := r.Start(t.Context(), host)
				want := setupHTTPError{stage: stagePreflight, status: status}
				if status == 429 || status == 503 {
					want.retryAt = started.Add(time.Minute)
				}
				if failure := asSetupHTTPError(err); failure == nil || *failure != want || err.Error() != fmt.Sprintf("connect to F1 live timing: negotiation preflight returned HTTP %d", status) {
					t.Fatalf("Start = %v", err)
				}
				if time.Now() != started || calls != 1 || r.done != nil || r.cancel != nil || *r.operational.state.Load() != (operationalState{}) || logs.Len() != 0 || len(host.events) != 0 || *body != (preflightTestBody{closes: 1}) {
					t.Fatal("failed Start entered runtime or lost cleanup")
				}
				select {
				case <-r.operational.stopDone:
				default:
					t.Fatal("cleanup not joined")
				}
				if r.operational.callbackEnabled {
					t.Fatal("failed Start retained callback")
				}
				if err := r.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestHTTPUpgradeTransportNetworkFailure(t *testing.T) {
	for _, cause := range []error{io.EOF, context.Canceled, context.DeadlineExceeded} {
		client := livenessWebSocketClient(t, func(*websocket.Conn) { t.Error("unexpected upgrade") })
		base := client.Transport
		client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet {
				return base.RoundTrip(request)
			}
			return nil, fmt.Errorf("private-network: %w", cause)
		})
		connection, err := connectSignalR(t.Context(), client, connectionTestConfig(t, "http://synthetic.test/private-route"))
		want := "SignalR WebSocket upgrade failed"
		if cause != io.EOF {
			want = "SignalR WebSocket upgrade: " + cause.Error()
		}
		if connection != nil || err == nil || err.Error() != want || asSetupHTTPError(err) != nil || errors.Is(err, errInvalidLiveTimingData) {
			t.Fatalf("connection=%v error=%v", connection, err)
		}
	}
}

func TestHTTPUpgradeTransportEmptyResponses(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *http.Response
		wantHTTP bool
	}{
		{name: "nil response remains a client error"},
		{name: "rejected response without body", response: &http.Response{StatusCode: http.StatusServiceUnavailable}, wantHTTP: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := livenessWebSocketClient(t, func(*websocket.Conn) { t.Error("unexpected upgrade") })
			base := client.Transport
			client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet {
					return base.RoundTrip(request)
				}
				return test.response, nil
			})
			connection, err := connectSignalR(t.Context(), client, connectionTestConfig(t, "http://synthetic.test/private-route"))
			if connection != nil || err == nil || errors.Is(err, errInvalidLiveTimingData) || errors.Unwrap(err) != nil {
				t.Fatalf("connection=%v error=%v", connection, err)
			}
			if test.wantHTTP {
				failure := asSetupHTTPError(err)
				if failure == nil || *failure != (setupHTTPError{stage: stageUpgrade, status: http.StatusServiceUnavailable}) {
					t.Fatalf("HTTP failure=%#v", failure)
				}
			} else if asSetupHTTPError(err) != nil || err.Error() != "SignalR WebSocket upgrade failed" {
				t.Fatalf("client error=%v", err)
			}
		})
	}
}
