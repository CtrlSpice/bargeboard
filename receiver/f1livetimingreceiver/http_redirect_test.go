package f1livetimingreceiver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

func TestHTTPRedirectClassificationBeforeLocationParsing(t *testing.T) {
	for _, stage := range []setupStage{stagePreflight, stageNegotiate} {
		for _, location := range []string{"%", "http://[::1", "https://private.test/redirect", ""} {
			t.Run(fmt.Sprintf("%s/%q", stage, location), func(t *testing.T) {
				body := &preflightTestBody{}
				header := http.Header{"Retry-After": {"60"}, "X-Private": {"private-header"}}
				if location != "" {
					header.Set("Location", location)
				}
				wantHeader := header.Clone()
				response := &http.Response{StatusCode: 302, Header: header, Body: body}
				wantResponse := *response
				calls, redirects := 0, 0
				client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return response, nil
				}), CheckRedirect: func(*http.Request, []*http.Request) error { redirects++; return http.ErrUseLastResponse }}
				var err error
				if stage == stagePreflight {
					var credentials connectionCredentials
					credentials, err = bootstrapConnection(t.Context(), client, connectionTestConfig(t, "http://synthetic.test/private-route"))
					if !reflect.DeepEqual(credentials, connectionCredentials{}) {
						t.Fatal("rejected preflight returned credentials")
					}
				} else {
					var result negotiation
					var cookies []*http.Cookie
					result, cookies, err = negotiate(t.Context(), client, "http://synthetic.test/private-route", connectionCredentials{}, nil)
					if result != (negotiation{}) || cookies != nil {
						t.Fatal("rejected negotiation returned state")
					}
				}
				if failure := asSetupHTTPError(err); failure == nil || *failure != (setupHTTPError{stage: stage, status: 302}) || !failure.permanent() || errors.Unwrap(err) != nil || errors.Is(err, errInvalidLiveTimingData) {
					t.Errorf("error=%v; want bare terminal HTTP 302 at %s", err, stage)
				}
				for _, format := range []string{"%v", "%+v", "%#v"} {
					if got := fmt.Sprintf(format, err); got != fmt.Sprintf("%s returned HTTP 302", stage) {
						t.Errorf("error formatting=%q", got)
					}
				}
				if calls != 1 || redirects != 0 || *body != (preflightTestBody{closes: 1}) {
					t.Errorf("calls=%d redirects=%d body=%+v", calls, redirects, body)
				}
				if !reflect.DeepEqual(header, wantHeader) || !reflect.DeepEqual(*response, wantResponse) {
					t.Fatal("response or headers mutated")
				}
			})
		}
	}
}

func TestHTTPRedirectPreflightCookieUsesConfiguredNegotiation(t *testing.T) {
	for _, location := range []string{"%", "http://[::1", "https://private.test/redirect", ""} {
		t.Run(fmt.Sprintf("%q", location), func(t *testing.T) {
			cfg := connectionTestConfig(t, "http://synthetic.test/configured")
			header := http.Header{"Set-Cookie": {"AWSALBCORS=synthetic-affinity; Path=/", "other=synthetic-other"}, "Retry-After": {"60"}}
			if location != "" {
				header.Set("Location", location)
			}
			wantHeader := header.Clone()
			preflightBody, negotiateBody := &preflightTestBody{}, &preflightTestBody{}
			response := &http.Response{StatusCode: 302, Status: "302 Found", Header: header, Body: preflightBody}
			wantResponse := *response
			var requests []string
			redirects := 0
			client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				requests = append(requests, request.Method+" "+request.URL.String())
				if len(requests) == 1 {
					return response, nil
				}
				if request.Header.Get("Authorization") != "Bearer subscription-token" || request.Header.Get("Cookie") != "AWSALBCORS=synthetic-affinity" {
					t.Error("negotiation credentials changed")
				}
				return &http.Response{StatusCode: 401, Body: negotiateBody}, nil
			}), CheckRedirect: func(*http.Request, []*http.Request) error { redirects++; return http.ErrUseLastResponse }}
			connection, err := connectSignalR(t.Context(), client, cfg)
			if failure := asSetupHTTPError(err); connection != nil || failure == nil || *failure != (setupHTTPError{stage: stageNegotiate, status: 401}) {
				t.Errorf("connection=%v error=%v; want configured negotiation's 401", connection, err)
			}
			wantRequests := []string{"OPTIONS " + cfg.NegotiateEndpoint, "POST " + cfg.NegotiateEndpoint + "?negotiateVersion=1"}
			if !reflect.DeepEqual(requests, wantRequests) || redirects != 0 {
				t.Errorf("requests=%v redirects=%d", requests, redirects)
			}
			if *preflightBody != (preflightTestBody{closes: 1}) || *negotiateBody != (preflightTestBody{closes: 1}) {
				t.Errorf("preflight=%+v negotiation=%+v", preflightBody, negotiateBody)
			}
			if !reflect.DeepEqual(header, wantHeader) || !reflect.DeepEqual(*response, wantResponse) {
				t.Fatal("original response or cookie headers mutated")
			}
		})
	}
}

func TestHTTPRedirectContextPriority(t *testing.T) {
	for _, stage := range []setupStage{stagePreflight, stageNegotiate} {
		for _, cookie := range []bool{false, true} {
			for _, deadline := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/cookie=%v/deadline=%v", stage, cookie, deadline), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						ctx, cancel := context.WithCancelCause(t.Context())
						defer cancel(nil)
						wantErr := context.Canceled
						if deadline {
							var stop context.CancelFunc
							ctx, stop = context.WithTimeoutCause(ctx, time.Second, errors.New("private-deadline-cause"))
							defer stop()
							wantErr = context.DeadlineExceeded
						}
						body := &preflightTestBody{}
						header := http.Header{"Location": {"%"}, "Retry-After": {"60"}}
						if cookie {
							header.Set("Set-Cookie", "AWSALBCORS=synthetic-affinity")
						}
						wantHeader := header.Clone()
						calls, redirects := 0, 0
						client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
							calls++
							if deadline {
								time.Sleep(time.Second)
							} else {
								cancel(errors.New("private-cancel-cause"))
							}
							return &http.Response{StatusCode: 302, Header: header, Body: body}, nil
						}), CheckRedirect: func(*http.Request, []*http.Request) error { redirects++; return http.ErrUseLastResponse }}
						var err error
						if stage == stagePreflight {
							var got connectionCredentials
							got, err = bootstrapConnection(ctx, client, connectionTestConfig(t, "http://synthetic.test"))
							if !reflect.DeepEqual(got, connectionCredentials{}) {
								t.Fatal("canceled preflight returned credentials")
							}
						} else {
							var got negotiation
							var cookies []*http.Cookie
							got, cookies, err = negotiate(ctx, client, "http://synthetic.test", connectionCredentials{}, nil)
							if got != (negotiation{}) || cookies != nil {
								t.Fatal("canceled negotiation returned state")
							}
						}
						operation := "perform " + stage.String()
						if !errors.Is(err, wantErr) || errors.Unwrap(err) != wantErr || err.Error() != operation+": "+wantErr.Error() || asSetupHTTPError(err) != nil {
							t.Fatalf("context error=%v", err)
						}
						if calls != 1 || redirects != 0 || *body != (preflightTestBody{closes: 1}) || !reflect.DeepEqual(header, wantHeader) {
							t.Fatalf("calls=%d redirects=%d body=%+v", calls, redirects, body)
						}
					})
				})
			}
		}
	}
}

func TestHTTPRedirectAcceptedResponsePreservesStatusAndOwnership(t *testing.T) {
	for _, status := range []int{300, 301, 302, 303, 304, 305, 306, 307, 308, 399} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			body := &preflightTestBody{}
			original := &http.Response{StatusCode: status, Status: fmt.Sprintf("%d synthetic status", status), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
				Header: http.Header{"Location": {"%"}, "Set-Cookie": {"AWSALBCORS=synthetic-affinity; Path=/"}, "X-Private": {"private-header"}}, Body: body, ContentLength: -1}
			before := *original
			before.Header = original.Header.Clone()
			transport := setupHTTPTransport{stage: stagePreflight, base: roundTripperFunc(func(*http.Request) (*http.Response, error) { return original, nil })}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodOptions, "http://synthetic.test", nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := transport.RoundTrip(request)
			want := before
			want.Header = before.Header.Clone()
			want.Header.Del("Location")
			if err != nil || got == original || !reflect.DeepEqual(got, &want) || !reflect.DeepEqual(original, &before) || *body != (preflightTestBody{}) {
				t.Fatalf("adapter changed status/ownership or original response: error=%v", err)
			}
			got.Header.Set("Set-Cookie", "other=changed")
			if !reflect.DeepEqual(original, &before) {
				t.Fatal("accepted headers alias original")
			}
			_ = got.Body.Close()
			if *body != (preflightTestBody{closes: 1}) {
				t.Fatal("accepted body ownership changed")
			}
		})
	}
}
