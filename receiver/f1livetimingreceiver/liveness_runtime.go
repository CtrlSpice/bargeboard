package f1livetimingreceiver

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// writeHubPings is the sole post-subscription application writer. The read
// lifecycle cancels and joins it, including when a callback rejects a batch.
// It owns lastOutbound; it never accesses the reader's manifests or budgets.
func writeHubPings(ctx context.Context, socket signalRSocket, lastOutbound time.Time) error {
	timer := time.NewTimer(hubPingDelay(lastOutbound, time.Now()))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return nil
		}
		// Reuse the server timeout as a technical write bound. Local work pauses
		// receive budgets, but must not leave a blocked ping alive indefinitely.
		writeCtx, cancel := context.WithTimeout(ctx, hubServerTimeout)
		err := socket.Write(writeCtx, websocket.MessageText, []byte(hubPingRecord))
		if err != nil {
			// A write deadline closes the socket and can wake the reader before
			// Write returns. Its ensuing cleanup must not erase that deadline.
			if ctx.Err() != nil && writeCtx.Err() != context.DeadlineExceeded {
				cancel()
				return nil // Read-lifecycle cleanup is not a ping failure.
			}
			err = sanitizedTransportError(writeCtx, "write SignalR ping", err)
			cancel()
			return err
		}
		lastOutbound = time.Now()
		cancel()
		timer.Reset(hubPingDelay(lastOutbound, time.Now()))
	}
}
