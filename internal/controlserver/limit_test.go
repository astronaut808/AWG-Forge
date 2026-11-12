package controlserver

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type closeTestListener struct {
	net.Listener
	calls atomic.Int32
	err   error
}

func (listener *closeTestListener) Close() error {
	if listener.calls.Add(1) != 1 {
		return net.ErrClosed
	}
	return listener.err
}

func TestBoundedListenerCloseOnce(t *testing.T) {
	for _, firstErr := range []error{nil, errors.New("listener close failure")} {
		name := "success"
		if firstErr != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			underlying := &closeTestListener{err: firstErr}
			listener := limitListener(underlying, 1)
			var wg sync.WaitGroup
			for i := 0; i < 32; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := listener.Close(); !errors.Is(err, firstErr) {
						t.Errorf("Close() = %v, want %v", err, firstErr)
					}
				}()
			}
			wg.Wait()
			if calls := underlying.calls.Load(); calls != 1 {
				t.Fatalf("underlying Close calls = %d, want 1", calls)
			}
			if err := listener.Close(); !errors.Is(err, firstErr) {
				t.Fatalf("repeated Close() = %v, want %v", err, firstErr)
			}
		})
	}
}

func TestBoundedListenerCloseUnblocksCapacityWait(t *testing.T) {
	underlying, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := limitListener(underlying, 1)
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	done := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		done <- err
	}()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept() = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Accept waiting for connection capacity")
	}
}
