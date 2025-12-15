package netpool

import (
	"context"
	"net"
	"sync/atomic"
	"time"
)

// BasicPool is a stripped-down lock-free pool without IdleTimeout or HealthCheck.
type BasicPool struct {
	idleConns      chan net.Conn
	factory        func() (net.Conn, error)
	numOpen        atomic.Int32
	maxPool        int32
	minPool        int32
	dialTimeout    time.Duration
	closed         atomic.Bool
	stopMaintainer chan struct{}
}

// NewBasic creates a new high-performance basic pool.
// Note: This pool ignores MaxIdleTime and HealthCheck in Config.
func NewBasic(factory func() (net.Conn, error), cfg Config) (*BasicPool, error) {
	if cfg.MaxPool <= 0 {
		cfg.MaxPool = 15
	}
	if cfg.MinPool < 0 {
		cfg.MinPool = 0
	}
	if cfg.MinPool > cfg.MaxPool {
		cfg.MinPool = cfg.MaxPool
	}

	pool := &BasicPool{
		idleConns:      make(chan net.Conn, cfg.MaxPool),
		factory:        factory,
		maxPool:        cfg.MaxPool,
		minPool:        cfg.MinPool,
		dialTimeout:    cfg.DialTimeout,
		stopMaintainer: make(chan struct{}),
	}

	for i := int32(0); i < cfg.MinPool; i++ {
		conn, err := pool.dial()
		if err != nil {
			pool.Close()
			return nil, err
		}
		pool.idleConns <- conn
		pool.numOpen.Add(1)
	}

	if cfg.MinPool > 0 {
		go pool.maintainer()
	}

	return pool, nil
}

func (p *BasicPool) dial() (net.Conn, error) {
	if p.dialTimeout > 0 {
		type result struct {
			conn net.Conn
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			conn, err := p.factory()
			ch <- result{conn, err}
		}()

		select {
		case r := <-ch:
			return r.conn, r.err
		case <-time.After(p.dialTimeout):
			return nil, ErrDialTimeout
		}
	}
	return p.factory()
}

// Get returns a connection from the pool.
func (p *BasicPool) Get() (net.Conn, error) {
	return p.GetWithContext(context.Background())
}

// GetWithContext returns a connection, blocking until one is available.
func (p *BasicPool) GetWithContext(ctx context.Context) (net.Conn, error) {
	if p.closed.Load() {
		return nil, ErrPoolClosed
	}

	select {
	case conn := <-p.idleConns:
		return conn, nil
	default:
	}

	for {
		if p.closed.Load() {
			return nil, ErrPoolClosed
		}

		current := p.numOpen.Load()
		if current < p.maxPool {
			if p.numOpen.CompareAndSwap(current, current+1) {
				conn, err := p.dial()
				if err != nil {
					p.numOpen.Add(-1)
					return nil, err
				}
				return conn, nil
			}
			continue
		}

		select {
		case conn := <-p.idleConns:
			return conn, nil
		case <-p.stopMaintainer:
			return nil, ErrPoolClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Put returns a connection to the pool.
func (p *BasicPool) Put(conn net.Conn) {
	if conn == nil {
		return
	}
	if p.closed.Load() {
		conn.Close()
		p.numOpen.Add(-1)
		return
	}
	
	select {
	case p.idleConns <- conn:
	default:
		conn.Close()
		p.numOpen.Add(-1)
	}
}

// PutWithError returns a connection, closing it if there was an error.
func (p *BasicPool) PutWithError(conn net.Conn, err error) {
	if conn == nil {
		return
	}
	if err != nil {
		conn.Close()
		p.numOpen.Add(-1)
		return
	}
	p.Put(conn)
}

// Close closes the pool and all connections.
func (p *BasicPool) Close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	close(p.stopMaintainer)
	for {
		select {
		case conn := <-p.idleConns:
			conn.Close()
			p.numOpen.Add(-1)
		default:
			return
		}
	}
}

// Stats returns pool statistics.
func (p *BasicPool) Stats() PoolStats {
	idle := len(p.idleConns)
	total := int(p.numOpen.Load())
	return PoolStats{
		Active:  total,
		Idle:    idle,
		InUse:   total - idle,
		MaxPool: p.maxPool,
		MinPool: p.minPool,
	}
}

// Len returns the number of idle connections.
func (p *BasicPool) Len() int {
	return len(p.idleConns)
}

func (p *BasicPool) maintainer() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopMaintainer:
			return
		case <-ticker.C:
			p.maintainMin()
		}
	}
}

func (p *BasicPool) maintainMin() {
	if p.closed.Load() {
		return
	}
	idle := len(p.idleConns)
	needed := int(p.minPool) - idle
	for i := 0; i < needed; i++ {
		current := p.numOpen.Load()
		if current >= p.maxPool {
			break
		}
		if p.numOpen.CompareAndSwap(current, current+1) {
			conn, err := p.dial()
			if err != nil {
				p.numOpen.Add(-1)
				continue
			}
			select {
			case p.idleConns <- conn:
			default:
				conn.Close()
				p.numOpen.Add(-1)
			}
		}
	}
}
