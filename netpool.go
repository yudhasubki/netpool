package netpool

import (
	"container/list"
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Netpooler defines the public interface for a TCP connection pool.
//
// It provides methods for acquiring and releasing connections, inspecting
// pool state, and shutting down the pool. All implementations must be
// safe for concurrent use.
type Netpooler interface {
	// Close shuts down the pool and closes all managed connections.
	//
	// After Close is called, all subsequent calls to Get or Put will
	// return an error. Close is idempotent.
	Close()

	// Get returns a pooled connection using a background context.
	//
	// The returned connection is automatically returned to the pool
	// when Close() is called on it.
	Get() (net.Conn, error)

	// GetWithContext returns a pooled connection, blocking until one
	// becomes available or the context is canceled.
	//
	// If the context is canceled while waiting, an error is returned.
	GetWithContext(ctx context.Context) (net.Conn, error)

	// Len returns the number of currently idle connections in the pool.
	Len() int

	// Put returns a connection to the pool.
	//
	// If err is non-nil, the connection is considered invalid and will
	// be closed and removed from the pool.
	//
	// This method is primarily intended for internal use. Users should
	// prefer calling Close() on connections returned by Get().
	Put(conn net.Conn, err error)

	// Stats returns a snapshot of the current pool state.
	Stats() PoolStats
}

// Netpool is a thread-safe TCP connection pool implementation.
//
// It manages a set of reusable net.Conn objects, enforcing maximum
// capacity limits, automatic idle cleanup, and safe concurrent access.
type Netpool struct {
	// cond is used to coordinate goroutines waiting for available
	// connections when the pool is exhausted.
	cond *sync.Cond

	// connections holds idle connections in FIFO order.
	connections *list.List

	// allConns tracks all active connections currently managed
	// by the pool, including both idle and in-use connections.
	allConns map[net.Conn]*connEntry

	// fn is the factory function used to create new connections.
	fn netpoolFunc

	// mu protects all shared pool state.
	mu *sync.Mutex

	// config holds the pool configuration parameters.
	config Config

	// closed indicates whether the pool has been closed.
	closed atomic.Bool

	// reaper periodically cleans up idle connections.
	reaper *time.Ticker

	// stopReaper signals background goroutines to exit.
	stopReaper chan struct{}
}

// connEntry represents a single connection managed by the pool.
//
// It tracks connection state and lifecycle metadata required for
// safe reuse and idle cleanup.
type connEntry struct {
	// conn is the underlying network connection.
	conn net.Conn

	// lastUsed records the last time this connection was returned
	// to the idle pool.
	lastUsed time.Time

	// returned indicates whether the connection is currently
	// in the idle pool.
	returned atomic.Bool
}

// PoolStats provides a snapshot of the pool's current state.
//
// All values represent instantaneous measurements and may change
// immediately after Stats() returns.
type PoolStats struct {
	// Active is the total number of connections currently
	// managed by the pool (idle + in-use).
	Active int

	// Idle is the number of currently idle connections
	// available for immediate use.
	Idle int

	// InUse is the number of connections currently checked
	// out by callers.
	InUse int

	// MaxPool is the configured maximum pool size.
	MaxPool int32

	// MinPool is the configured minimum idle pool size.
	MinPool int32
}

type netpoolFunc func() (net.Conn, error)

func New(fn netpoolFunc, opts ...Opt) (*Netpool, error) {
	config := Config{
		MaxPool: 15,
		MinPool: 5,
	}

	for _, opt := range opts {
		opt(&config)
	}

	if config.MinPool < 0 {
		return nil, ErrInvalidConfig
	}

	if config.MaxPool < config.MinPool {
		return nil, ErrInvalidConfig
	}

	if config.MaxPool == 0 {
		return nil, ErrInvalidConfig
	}

	netpool := &Netpool{
		fn:          fn,
		mu:          new(sync.Mutex),
		connections: list.New(),
		allConns:    make(map[net.Conn]*connEntry),
		config:      config,
		stopReaper:  make(chan struct{}),
	}

	netpool.cond = sync.NewCond(netpool.mu)

	// Create initial MinPool connections
	for i := int32(0); i < config.MinPool; i++ {
		conn, err := netpool.createConnection()
		if err != nil {
			netpool.Close()
			return nil, err
		}

		entry := &connEntry{
			conn:     conn,
			lastUsed: time.Now(),
		}
		entry.returned.Store(true) // Mark as in pool

		netpool.connections.PushBack(entry)
		netpool.allConns[conn] = entry
	}

	// Start idle connection reaper
	if config.MaxIdleTime > 0 {
		netpool.startIdleReaper()
	}

	// Start idle connection maintainer (keeps MinPool alive)
	if config.MinPool > 0 {
		netpool.startIdleMaintainer()
	}

	return netpool, nil
}

func (netpool *Netpool) createConnection() (net.Conn, error) {
	conn, err := netpool.fn()
	if err != nil {
		return nil, err
	}

	if len(netpool.config.DialHooks) > 0 {
		for _, dialHook := range netpool.config.DialHooks {
			if err = dialHook(conn); err != nil {
				conn.Close()
				return nil, err
			}
		}
	}

	return conn, nil
}

func (netpool *Netpool) Get() (net.Conn, error) {
	return netpool.GetWithContext(context.Background())
}

func (netpool *Netpool) GetWithContext(ctx context.Context) (net.Conn, error) {
	conn, err := netpool.getWithContext(ctx)
	if err != nil {
		return nil, err
	}

	return newPooledConn(conn, netpool), nil
}

func (netpool *Netpool) getWithContext(ctx context.Context) (net.Conn, error) {
	if netpool.closed.Load() {
		return nil, ErrPoolClosed
	}

	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	for {
		if netpool.closed.Load() {
			return nil, ErrPoolClosed
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if netpool.connections.Len() > 0 {
			entry := netpool.connections.Remove(netpool.connections.Front()).(*connEntry)
			entry.lastUsed = time.Now()
			entry.returned.Store(false)
			return entry.conn, nil
		}

		if len(netpool.allConns) < int(netpool.config.MaxPool) {
			c, err := netpool.createConnection()
			if err != nil {
				return nil, err
			}
			entry := &connEntry{
				conn:     c,
				lastUsed: time.Now(),
			}
			entry.returned.Store(false)
			netpool.allConns[c] = entry
			return c, nil
		}

		done := make(chan struct{})

		go func() {
			select {
			case <-ctx.Done():
				netpool.mu.Lock()
				netpool.cond.Broadcast()
				netpool.mu.Unlock()
			case <-done:
			}
		}()

		netpool.cond.Wait()
		close(done)
	}
}

func (netpool *Netpool) Put(conn net.Conn, err error) {
	if conn == nil {
		return
	}

	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	entry, exists := netpool.allConns[conn]
	if !exists {
		_ = conn.Close()
		return
	}

	if !entry.returned.CompareAndSwap(false, true) {
		if err != nil {
			_ = conn.Close()
			delete(netpool.allConns, conn)
			netpool.cond.Signal()
		}
		return
	}

	// Pool closed -> cleanup
	if netpool.closed.Load() {
		_ = conn.Close()
		delete(netpool.allConns, conn)
		netpool.cond.Broadcast()
		return
	}

	// If conn is bad, drop it.
	if err != nil {
		_ = conn.Close()
		delete(netpool.allConns, conn)
		netpool.cond.Signal()
		return
	}

	// If we are above MaxPool (shouldn't happen often, but be safe)
	if len(netpool.allConns) > int(netpool.config.MaxPool) {
		_ = conn.Close()
		delete(netpool.allConns, conn)
		netpool.cond.Signal()
		return
	}

	// Return to pool
	entry.lastUsed = time.Now()
	netpool.connections.PushBack(entry)
	netpool.cond.Signal()
}

func (netpool *Netpool) Close() {
	if !netpool.closed.CompareAndSwap(false, true) {
		return
	}

	close(netpool.stopReaper)

	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	if netpool.reaper != nil {
		netpool.reaper.Stop()
	}

	for n := netpool.connections.Front(); n != nil; n = n.Next() {
		entry := n.Value.(*connEntry)
		entry.conn.Close()
	}
	netpool.connections.Init()

	for conn, entry := range netpool.allConns {
		entry.conn.Close()
		delete(netpool.allConns, conn)
	}

	netpool.cond.Broadcast()
}

func (netpool *Netpool) Stats() PoolStats {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	idle := netpool.connections.Len()
	active := len(netpool.allConns)

	return PoolStats{
		Active:  active,
		Idle:    idle,
		InUse:   active - idle,
		MaxPool: netpool.config.MaxPool,
		MinPool: netpool.config.MinPool,
	}
}

func (netpool *Netpool) Len() int {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()
	return netpool.connections.Len()
}

func (netpool *Netpool) startIdleReaper() {
	netpool.reaper = time.NewTicker(netpool.config.MaxIdleTime / 2)

	go func() {
		for {
			select {
			case <-netpool.reaper.C:
				netpool.reapIdleConnections()
			case <-netpool.stopReaper:
				return
			}
		}
	}()
}

func (netpool *Netpool) startIdleMaintainer() {
	go func() {
		interval := netpool.calculateMaintainerInterval()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				netpool.maintainMinPool()
			case <-netpool.stopReaper:
				return
			}
		}
	}()
}

func (netpool *Netpool) calculateMaintainerInterval() time.Duration {
	if netpool.config.MaintainerInterval > 0 {
		return netpool.config.MaintainerInterval
	}

	if netpool.config.MaxIdleTime > 0 && netpool.config.MaxIdleTime < 10*time.Second {
		return netpool.config.MaxIdleTime
	}

	if netpool.config.MaxIdleTime > 0 && netpool.config.MaxIdleTime < 1*time.Minute {
		return 5 * time.Second
	}

	return 30 * time.Second
}

// maintainMinPool ensures we always have MinPool idle connections
func (netpool *Netpool) maintainMinPool() {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	if netpool.closed.Load() {
		return
	}

	idle := netpool.connections.Len()
	active := len(netpool.allConns)

	// Maintain *idle* minimum = MinPool
	neededIdle := int(netpool.config.MinPool) - idle
	if neededIdle <= 0 {
		return
	}

	// Cap by remaining capacity to MaxPool
	remaining := int(netpool.config.MaxPool) - active
	if remaining <= 0 {
		return
	}
	if neededIdle > remaining {
		neededIdle = remaining
	}

	added := 0
	for i := 0; i < neededIdle; i++ {
		conn, err := netpool.createConnection()
		if err != nil {
			continue
		}

		entry := &connEntry{
			conn:     conn,
			lastUsed: time.Now(),
		}
		entry.returned.Store(true)

		netpool.connections.PushBack(entry)
		netpool.allConns[conn] = entry
		added++
	}

	if added > 0 {
		// Wake anyone waiting for a conn
		netpool.cond.Broadcast()
	}
}

// reapIdleConnections removes connections that have been idle too long
func (netpool *Netpool) reapIdleConnections() {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	if netpool.closed.Load() {
		return
	}

	now := time.Now()
	var toRemove []*list.Element

	for e := netpool.connections.Front(); e != nil; e = e.Next() {
		entry := e.Value.(*connEntry)
		if now.Sub(entry.lastUsed) > netpool.config.MaxIdleTime {
			if netpool.connections.Len()-len(toRemove) <= int(netpool.config.MinPool) {
				break
			}
			toRemove = append(toRemove, e)
		}
	}

	for _, e := range toRemove {
		entry := e.Value.(*connEntry)
		netpool.connections.Remove(e)
		entry.conn.Close()
		delete(netpool.allConns, entry.conn)
	}

	if len(toRemove) > 0 {
		netpool.cond.Broadcast()
	}
}
