package netpool

import (
	"context"
	"net"
	"sync/atomic"
	"time"
)

// Netpooler defines the public interface for a TCP connection pool.
type Netpooler interface {
	Close()
	Get() (net.Conn, error)
	GetWithContext(ctx context.Context) (net.Conn, error)
	Len() int
	Put(conn net.Conn)
	PutWithError(conn net.Conn, err error)
	Stats() PoolStats
}

// Config holds configuration for the connection pool.
type Config struct {
	// MaxPool is the maximum number of connections (default: 15)
	MaxPool int32

	// MinPool is the minimum idle connections to maintain (default: 0)
	MinPool int32

	// DialTimeout for connection creation (optional)
	DialTimeout time.Duration

	// MaxIdleTime is the maximum duration a connection can remain idle.
	// Connections idle longer than this are closed. Zero disables idle timeout.
	MaxIdleTime time.Duration

	// HealthCheck is called before returning a connection from Get().
	// If it returns an error, the connection is discarded and a new one is obtained.
	// This adds latency but ensures connections are valid.
	HealthCheck func(conn net.Conn) error
}

// PoolStats provides a snapshot of the pool's current state.
type PoolStats struct {
	Active  int
	Idle    int
	InUse   int
	MaxPool int32
	MinPool int32
}

// idleConn wraps a connection with its idle timestamp.
// Passed by value to avoid allocations and sync.Pool overhead.
type idleConn struct {
	conn      net.Conn
	idleSince time.Time
}

// Netpool is a lock-free TCP connection pool using Go channels.
// No mutex, no map - just channels and atomics for maximum performance.
type Netpool struct {
	// idleConns stores idleConn by value to avoid GC overhead
	idleConns      chan idleConn
	sem            chan struct{}
	factory        func(context.Context) (net.Conn, error)
	healthCheck    func(conn net.Conn) error
	numOpen        atomic.Int32
	maxPool        int32
	minPool        int32
	maxIdleTime    time.Duration
	dialTimeout    time.Duration
	closed         atomic.Bool
	stopMaintainer chan struct{}
}

// New creates a new lock-free connection pool.
func New(factory func(context.Context) (net.Conn, error), cfg Config) (*Netpool, error) {
	if cfg.MaxPool <= 0 {
		cfg.MaxPool = 15
	}
	if cfg.MinPool < 0 {
		cfg.MinPool = 0
	}
	if cfg.MinPool > cfg.MaxPool {
		cfg.MinPool = cfg.MaxPool
	}

	pool := &Netpool{
		idleConns:      make(chan idleConn, cfg.MaxPool),
		sem:            make(chan struct{}, cfg.MaxPool),
		factory:        factory,
		healthCheck:    cfg.HealthCheck,
		maxPool:        cfg.MaxPool,
		minPool:        cfg.MinPool,
		maxIdleTime:    cfg.MaxIdleTime,
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

			pool.idleConns <- idleConn{
				conn:      conn,
				idleSince: time.Now(),
			}
			pool.numOpen.Add(1)
		default:
		}
	}

	// Start maintainer for MinPool and idle timeout
	if cfg.MinPool > 0 || cfg.MaxIdleTime > 0 {
		go pool.maintainer()
	}

	return pool, nil
}

func (p *Netpool) dial(ctx context.Context) (net.Conn, error) {
	if p.dialTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.dialTimeout)
		defer cancel()
	}
	return p.factory(ctx)
}

// Get returns a connection from the pool.
func (p *Netpool) Get() (net.Conn, error) {
	return p.GetWithContext(context.Background())
}

// GetWithContext returns a connection, blocking until one is available or ctx is cancelled.
func (p *Netpool) GetWithContext(ctx context.Context) (net.Conn, error) {
	if p.closed.Load() {
		return nil, ErrPoolClosed
	}

	select {
	case ic := <-p.idleConns:
		conn := p.validateConnection(ic)
		if conn != nil {
			return conn, nil
		}
	default:
	}

	for {
		if p.closed.Load() {
			return nil, ErrPoolClosed
		}

		select {
		case ic := <-p.idleConns:
			conn := p.validateConnection(ic)
			if conn != nil {
				return conn, nil
			}
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
}

// validateConnection checks if a connection is still valid
func (p *Netpool) validateConnection(ic idleConn) net.Conn {
	if p.maxIdleTime > 0 && time.Since(ic.idleSince) > p.maxIdleTime {
		ic.conn.Close()
		p.numOpen.Add(-1)
		p.sem <- struct{}{}
		return nil
	}

	if p.healthCheck != nil {
		if err := p.healthCheck(ic.conn); err != nil {
			ic.conn.Close()
			p.numOpen.Add(-1)
			p.sem <- struct{}{}
			return nil
		}
	}

	return ic.conn
}

// Put returns a connection to the pool.
func (p *Netpool) Put(conn net.Conn) {
	if conn == nil {
		return
	}
	if p.closed.Load() {
		conn.Close()
		p.numOpen.Add(-1)
		p.sem <- struct{}{}
		return
	}

	select {
	case p.idleConns <- idleConn{conn: conn, idleSince: time.Now()}:
	default:
		conn.Close()
		p.numOpen.Add(-1)
		p.sem <- struct{}{}
	}
}

// PutWithError returns a connection, closing it if there was an error.
func (p *Netpool) PutWithError(conn net.Conn, err error) {
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
func (p *Netpool) Close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	close(p.stopMaintainer)
	for {
		select {
		case ic := <-p.idleConns:
			ic.conn.Close()
			p.numOpen.Add(-1)
		default:
			return
		}
	}
}

// Stats returns pool statistics.
func (p *Netpool) Stats() PoolStats {
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
func (p *Netpool) Len() int {
	return len(p.idleConns)
}

func (p *Netpool) maintainer() {
	interval := 5 * time.Second
	if p.maxIdleTime > 0 && p.maxIdleTime < interval {
		interval = p.maxIdleTime / 2
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopMaintainer:
			return
		case <-ticker.C:
			p.reapIdle()
			p.maintainMin()
		}
	}
}

// ReapIdle removes connections that have been idle too long.
func (p *Netpool) reapIdle() {
	if p.closed.Load() || p.maxIdleTime == 0 {
		return
	}

	n := len(p.idleConns)
	if n == 0 {
		return
	}

	now := time.Now()

	for i := 0; i < n; i++ {
		select {
		case ic := <-p.idleConns:
			if now.Sub(ic.idleSince) > p.maxIdleTime {
				if int(p.numOpen.Load()) <= int(p.minPool) {
					p.putBack(ic)
					continue
				}

				ic.conn.Close()
				p.numOpen.Add(-1)
				p.sem <- struct{}{}
			} else {
				p.putBack(ic)
			}
		default:
			return
		}
	}
}

func (p *Netpool) putBack(ic idleConn) {
	select {
	case p.idleConns <- ic:
	default:
		ic.conn.Close()
		p.numOpen.Add(-1)
		p.sem <- struct{}{}
	}
}

func (p *Netpool) maintainMin() {
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

			p.numOpen.Add(1)

			select {
			case p.idleConns <- idleConn{conn: conn, idleSince: time.Now()}:
			default:
				conn.Close()
				p.numOpen.Add(-1)
				p.sem <- struct{}{}
			}
		default:
			return
		}
	}
}
