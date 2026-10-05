package controlserver

import (
	"net"
	"sync"
)

type boundedListener struct {
	net.Listener
	slots    chan struct{}
	closed   chan struct{}
	once     sync.Once
	closeErr error
}

func limitListener(listener net.Listener, maximum int) net.Listener {
	return &boundedListener{Listener: listener, slots: make(chan struct{}, maximum), closed: make(chan struct{})}
}

func (listener *boundedListener) Accept() (net.Conn, error) {
	select {
	case listener.slots <- struct{}{}:
	case <-listener.closed:
		return nil, net.ErrClosed
	}
	conn, err := listener.Listener.Accept()
	if err != nil {
		<-listener.slots
		return nil, err
	}
	return &boundedConn{Conn: conn, release: func() { <-listener.slots }}, nil
}

func (listener *boundedListener) Close() error {
	listener.once.Do(func() {
		close(listener.closed)
		listener.closeErr = listener.Listener.Close()
	})
	return listener.closeErr
}

type boundedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (conn *boundedConn) Close() error {
	err := conn.Conn.Close()
	conn.once.Do(conn.release)
	return err
}
