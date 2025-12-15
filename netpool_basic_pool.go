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
	sem            chan struct{}
	factory        func(context.Context) (net.Conn, error)
	numOpen        atomic.Int32
	maxPool        int32
	minPool        int32
	dialTimeout    time.Duration
	closed         atomic.Bool
	stopMaintainer chan struct{}
}

// NewBasic creates a new high-performance basic pool.
// Note: This pool ignores MaxIdleTime and HealthCheck in Config.
func NewBasic(factory func(context.Context) (net.Conn, error), cfg Config) (*BasicPool, error) {
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
		sem:            make(chan struct{}, cfg.MaxPool),
		factory:        factory,
		maxPool:        cfg.MaxPool,
		minPool:        cfg.MinPool,
		dialTimeout:    cfg.DialTimeout,
		stopMaintainer: make(chan struct{}),
	}

	// Initialize semaphore
	for i := int32(0); i < cfg.MaxPool; i++ {
		pool.sem <- struct{}{}
	}

	for i := int32(0); i < cfg.MinPool; i++ {
		select {
		case <-pool.sem:
			conn, err := pool.dial(context.Background())
			if err != nil {
				pool.sem <- struct{}{}
				pool.Close()
				return nil, err
			}
			pool.idleConns <- conn
			pool.numOpen.Add(1)
		default:
		}
	}

	if cfg.MinPool > 0 {
		go pool.maintainer()
	}

	return pool, nil
}

func (p *BasicPool) dial(ctx context.Context) (net.Conn, error) {
	if p.dialTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.dialTimeout)
		defer cancel()
	}
	return p.factory(ctx)
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

	// Fast path
	select {
	case conn := <-p.idleConns:
		return conn, nil
	default:
	}

	select {
	case conn := <-p.idleConns:
		return conn, nil
	case <-p.sem:
		conn, err := p.dial(ctx)
		if err != nil {
			p.sem <- struct{}{}
			return nil, err
		}
		p.numOpen.Add(1)
		return conn, nil
	case <-p.stopMaintainer:
		return nil, ErrPoolClosed
	case <-ctx.Done():
		return nil, ctx.Err()
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
		p.sem <- struct{}{}
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
		select {
		case p.sem <- struct{}{}:
		default:
		}
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
		select {
		case <-p.sem:
			conn, err := p.dial(context.Background())
			if err != nil {
				p.sem <- struct{}{}
				continue
			}
			select {
			case p.idleConns <- conn:
				p.numOpen.Add(1)
			default:
				conn.Close()
				p.sem <- struct{}{}
			}
		default:
			return
		}
	}
}
