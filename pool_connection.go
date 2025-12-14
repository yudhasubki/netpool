package netpool

import (
	"net"
	"sync"
	"sync/atomic"
)

var pooledConnPool = sync.Pool{
	New: func() any {
		return new(pooledConn)
	},
}

// pooledConn wraps a net.Conn and automatically returns it to the pool on Close()
type pooledConn struct {
	net.Conn
	pool     *Netpool
	returned atomic.Bool

	// pointer to last non-nil error from Read/Write
	lastErr atomic.Pointer[error]
}

func newPooledConn(c net.Conn, p *Netpool) *pooledConn {
	pc := pooledConnPool.Get().(*pooledConn)
	pc.Conn = c
	pc.pool = p
	pc.returned.Store(false)
	pc.lastErr.Store(nil)
	return pc
}

func (pc *pooledConn) setErr(err error) {
	if err == nil {
		return
	}
	pc.lastErr.CompareAndSwap(nil, &err)
}

// Close returns the connection to the pool instead of closing it
func (pc *pooledConn) Close() error {
	if !pc.returned.CompareAndSwap(false, true) {
		return nil
	}

	var err error
	if v := pc.lastErr.Load(); v != nil {
		err = *v
	}
	pc.pool.Put(pc.Conn, err)

	pc.Conn = nil
	pc.pool = nil
	pooledConnPool.Put(pc)

	return nil
}

// MarkUnusable marks the connection as unusable
func (pc *pooledConn) MarkUnusable() error {
	if !pc.returned.CompareAndSwap(false, true) {
		return nil
	}

	pc.pool.Put(pc.Conn, ErrInvalidConn)
	return nil
}

// Read implements net.Conn.Read
func (pc *pooledConn) Read(b []byte) (n int, err error) {
	if pc.returned.Load() {
		return 0, ErrConnReturned
	}

	n, err = pc.Conn.Read(b)
	if err != nil {
		pc.setErr(err)
	}
	return n, err
}

// Write implements net.Conn.Write
func (pc *pooledConn) Write(b []byte) (n int, err error) {
	if pc.returned.Load() {
		return 0, ErrConnReturned
	}

	n, err = pc.Conn.Write(b)
	if err != nil {
		pc.setErr(err)
	}
	return n, err
}
