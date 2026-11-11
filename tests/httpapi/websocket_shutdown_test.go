package httpapi_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"

	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
)

func TestWebSocketCloseDoesNotCancelQueuedMessages(t *testing.T) {
	sends := make(chan string, 4)
	release := make(chan struct{})

	cfg := sendConfig()
	cfg.SendTimeout = 5 * time.Second

	srv, api := newSendGateway(t, cfg, funcSender{
		fn: func(context.Context, message.Message) (message.Receipt, error) {
			sends <- "sent"
			<-release
			return message.Receipt{MessageID: "m", SentAt: time.Now()}, nil
		},
	})

	conn := dialWebSocket(t, srv, testAccountToken)
	_ = readFrame(t, conn) // ready

	writeFrame(t, conn, sendFramePayload("r-1", "one"))
	select {
	case <-sends:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first send")
	}

	// The first send is blocked inside the sender, so the second message stays
	// queued. Both acknowledgements prove both are enqueued.
	writeFrame(t, conn, sendFramePayload("r-2", "two"))
	_ = decodeFrame[queuedFrame](t, readFrame(t, conn))
	_ = decodeFrame[queuedFrame](t, readFrame(t, conn))

	// Answer the coming close frame so the server's close handshake finishes.
	readDone := make(chan error, 1)
	go func() {
		_, _, err := conn.Read(context.Background())
		readDone <- err
	}()

	closed := make(chan struct{})
	go func() {
		api.CloseWebSockets()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseWebSockets did not return while a send was in flight")
	}

	select {
	case err := <-readDone:
		if websocket.CloseStatus(err) != websocket.StatusGoingAway {
			t.Fatalf("close status = %v, want %v (error %v)",
				websocket.CloseStatus(err), websocket.StatusGoingAway, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the close frame")
	}

	// The connection is gone; the queue is not.
	close(release)

	select {
	case <-sends:
	case <-time.After(2 * time.Second):
		t.Fatal("queued message was not sent after the connection closed")
	}
}

func TestWebSocketConnectionChurnDoesNotLeakGoroutines(t *testing.T) {
	srv, api := newWebSocketServer(t, httpapi.Options{})
	baseline := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		conn := dialWebSocket(t, srv, testAccountToken)
		_ = readFrame(t, conn) // ready
		if err := conn.CloseNow(); err != nil {
			t.Fatalf("connection %d CloseNow() error = %v", i, err)
		}
	}

	// CloseWebSockets waits for every connection handler to return.
	api.CloseWebSockets()

	deadline := time.Now().Add(2 * time.Second)
	for {
		goroutines := runtime.NumGoroutine()
		if goroutines <= baseline+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want at most %d after 20 connections", goroutines, baseline+2)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
