package f1livetimingreceiver

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
)

// Use the real WebSocket codec over net.Pipe so synctest owns I/O and time.
// The recorder/hijacker technique follows coder/websocket v1.8.15's
// internal/test/wstest/pipe.go; all HTTP and hub data here are synthetic.
type livenessHijacker struct {
	*httptest.ResponseRecorder
	peer net.Conn
}

func (h livenessHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.peer, bufio.NewReadWriter(bufio.NewReader(h.peer), bufio.NewWriter(h.peer)), nil
}

func livenessWebSocketClient(t *testing.T, serve func(*websocket.Conn)) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		switch request.Method {
		case http.MethodOptions:
			http.SetCookie(recorder, &http.Cookie{Name: affinityCookieName, Value: "synthetic-affinity"})
			recorder.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodPost:
			_, _ = recorder.WriteString(`{"connectionId":"synthetic-id","connectionToken":"synthetic-token","negotiateVersion":1,"availableTransports":[{"transport":"WebSockets","transferFormats":["Text"]}]}`)
		case http.MethodGet:
			client, peer := net.Pipe()
			connection, err := websocket.Accept(livenessHijacker{recorder, peer}, request, nil)
			if err != nil {
				_ = client.Close()
				_ = peer.Close()
				return nil, err
			}
			go func() {
				defer connection.CloseNow()
				serve(connection)
			}()
			response := recorder.Result()
			response.Body = client
			return response, nil
		default:
			t.Errorf("unexpected method %s", request.Method)
		}
		return recorder.Result(), nil
	})}
}

func TestLivenessWebSocketSuccessfulReadCancellationAndControlPings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer := make(chan *websocket.Conn, 1)
		var pings []string
		peerDone := make(chan struct{})
		client := livenessWebSocketClient(t, func(socket *websocket.Conn) {
			defer close(peerDone)
			_, contents, err := socket.Read(t.Context())
			wantSubscribe, _ := encodeSubscribeInvocation(subscriptionTopics())
			if err != nil || string(contents) != string(wantSubscribe) {
				t.Errorf("subscribe = %q, %v", contents, err)
				return
			}
			peer <- socket
			for {
				kind, contents, err := socket.Read(t.Context())
				if err != nil {
					return
				}
				if kind != websocket.MessageText {
					t.Error("non-text outbound ping")
				}
				pings = append(pings, string(contents))
			}
		})
		socket, _, err := websocket.Dial(t.Context(), "ws://synthetic.test/hub", &websocket.DialOptions{HTTPClient: client})
		if err != nil {
			t.Fatal(err)
		}
		defer socket.CloseNow()
		connection := &signalRConnection{conn: socket}
		if err := connection.subscribe(t.Context()); err != nil {
			t.Fatal(err)
		}
		server := <-peer
		var batches []liveTimingBatch
		result := startLivenessRead(t.Context(), connection, func(_ context.Context, batch liveTimingBatch) error {
			batches = append(batches, batch)
			return nil
		})
		write := func(contents string) {
			t.Helper()
			if err := server.Write(t.Context(), websocket.MessageText, []byte(contents)); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
		}
		write(`{"type":3,"invocationId":"0"}` + "\x1e")
		time.Sleep(5 * time.Second)
		write(incrementalFeedA)
		time.Sleep(5 * time.Second)
		write(incrementalFeedC)
		// All three successful Read child contexts have now been canceled. The
		// socket must still send hub pings and answer WebSocket control pings.
		time.Sleep(10 * time.Second)
		if err := server.Ping(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second)
		if err := server.Ping(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10*time.Second - 1)
		requireLivenessRunning(t, result)
		time.Sleep(1)
		requireLivenessResult(t, result, errSignalRReceiveTimeout)
		<-peerDone
		want := []liveTimingBatch{
			{source: liveTimingUpdateSourceSnapshot, requestedTopics: subscriptionTopics(), presentTopics: []string{}, updates: []liveTimingUpdate{}},
			incrementalWantFeed("Started", "2026-08-21T10:30:00Z"),
			incrementalWantFeed("Finished", "2026-08-21T10:31:00Z"),
		}
		if !reflect.DeepEqual(batches, want) || !reflect.DeepEqual(pings, []string{hubPingRecord, hubPingRecord}) {
			t.Fatalf("batches = %#v, pings = %q", batches, pings)
		}
	})
}

func TestLivenessWebSocketPingWriteTimeoutClosesBlockedRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer := make(chan *websocket.Conn, 1)
		release := make(chan struct{})
		defer close(release)
		client := livenessWebSocketClient(t, func(socket *websocket.Conn) {
			if _, _, err := socket.Read(t.Context()); err != nil {
				t.Error(err)
				return
			}
			peer <- socket
			<-release // Deliberately never read the client's hub ping.
		})
		socket, _, err := websocket.Dial(t.Context(), "ws://synthetic.test/hub", &websocket.DialOptions{HTTPClient: client})
		if err != nil {
			t.Fatal(err)
		}
		defer socket.CloseNow()
		connection := &signalRConnection{conn: socket}
		if err := connection.subscribe(t.Context()); err != nil {
			t.Fatal(err)
		}
		server := <-peer
		connection.pending = []byte(`{"type":3,"invocationId":"0"}` + "\x1e")
		result := startLivenessRead(t.Context(), connection, func(context.Context, liveTimingBatch) error { return nil })
		for range 4 {
			time.Sleep(10 * time.Second)
			if err := server.Write(t.Context(), websocket.MessageText, []byte(hubPingRecord)); err != nil {
				t.Fatal(err)
			}
			requireLivenessRunning(t, result)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		err = <-result
		if err == nil || err.Error() != "write SignalR ping: context deadline exceeded" {
			t.Fatalf("write timeout = %v", err)
		}
	})
}
