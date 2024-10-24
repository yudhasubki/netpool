package netpool

import (
	"container/list"
	"net"
	"sync"
	"time"
)

type Netpooler interface {
	Close()
	Get() (net.Conn, error)
	Len() int
	Put(conn net.Conn, err error)
}

// Handle for the connection pool.
type Netpool struct {
	actives     int32
	counter     int64
	cond        *sync.Cond
	connections *list.List
	fn          netpoolFunc
	mu          *sync.Mutex
	config      config
	last        time.Time
}

type netpoolFunc func() (net.Conn, error)

// New will create a netpool with the connection creation function and options
// as desired.  The fn is a function handle to call for creation of a new conn,
// and then opts sets the desired state of the netpool.
func New(fn netpoolFunc, opts ...Opt) (*Netpool, error) {
	config := config{
		MaxPool: 15,
		MinPool: 5,
	}

	for _, opt := range opts {
		opt(&config)
	}

	netpool := &Netpool{
		fn:          fn,
		mu:          new(sync.Mutex),
		actives:     0,
		connections: list.New(),
		config:      config,
		last:        time.Now(),
	}

	for i := int32(0); i < config.MinPool; i++ {
		conn, err := netpool.fn()
		if err != nil {
			return netpool, err
		}

		if len(netpool.config.DialHooks) > 0 {
			for _, dialHook := range netpool.config.DialHooks {
				err = dialHook(conn)
				if err != nil {
					conn.Close()
					return netpool, err
				}
			}
		}

		netpool.connections.PushBack(conn)
		netpool.actives++
		netpool.counter++
	}
	netpool.cond = sync.NewCond(netpool.mu)

	if config.CheckTime > time.Second/4 {
		go func() {
			for {
				// Wait a given timestep
				time.Sleep(config.CheckTime)
				netpool.mu.Lock()

				// If there hasn't been much use, close all connections beyond the
				// minimum required pooled connections.
				if time.Now().Sub(netpool.last) > config.CullTime {
					for netpool.connections.Len() > int(config.MinPool) {
						conn := netpool.connections.Remove(netpool.connections.Front()).(net.Conn)
						conn.Close()
						netpool.actives--
					}
				}

				// Check one connection every time step.  Eventually any dead
				// connections will be weeded out.
				if netpool.connections.Len() > 0 {
					conn := netpool.connections.Remove(netpool.connections.Front()).(net.Conn)
					err := connCheck(conn)
					if err != nil {
						if conn != nil {
							conn.Close()
							netpool.actives--
						}
					} else {
						netpool.connections.PushBack(conn)
					}
				}

				// Maintain a minimum of connections in a "ready" state.
			fillPool:
				for netpool.connections.Len() < int(config.MinPool) {
					conn, err := netpool.fn()
					if err != nil {
						break
					}

					if len(netpool.config.DialHooks) > 0 {
						for _, dialHook := range netpool.config.DialHooks {
							err = dialHook(conn)
							if err != nil {
								conn.Close()
								break fillPool
							}
						}
					}

					netpool.connections.PushBack(conn)
					netpool.actives++
					netpool.counter++
				}
				netpool.mu.Unlock()
			}
		}()
	}

	return netpool, nil
}

// Get a net.conn to work with.  If a used connection returned is checked to be
// ready for use, a new connection will be created if none is available, or an
// error will be returned.
func (netpool *Netpool) Get() (net.Conn, error) {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()
	netpool.last = time.Now()

	for netpool.connections.Len() == 0 && netpool.actives >= netpool.config.MaxPool {
		netpool.cond.Wait()
	}

	for netpool.connections.Len() > 0 {
		conn := netpool.connections.Remove(netpool.connections.Front()).(net.Conn)
		err := connCheck(conn)
		if err == nil {
			return conn, nil
		}
		conn.Close()
		netpool.actives--
	}

	c, err := netpool.fn()
	if err != nil {
		return nil, err
	}

	if len(netpool.config.DialHooks) > 0 {
		for _, dialHook := range netpool.config.DialHooks {
			err = dialHook(c)
			if err != nil {
				c.Close()
				return nil, err
			}
		}
	}

	netpool.actives++
	netpool.counter++

	return c, nil
}

// Put a connection back on the pool.  If an error is passed in then the active
// count will be reduced and not returned to the pool.
func (netpool *Netpool) Put(conn net.Conn, err error) {
	// Verify that the connection is in a good state before returning it to the pool
	if err == nil {
		err = connCheck(conn)
	}

	netpool.mu.Lock()
	defer netpool.mu.Unlock()
	if err != nil {
		if conn != nil {
			conn.Close()
			netpool.actives--
		}
	} else {
		netpool.connections.PushBack(conn)
	}

	netpool.last = time.Now()
	netpool.cond.Signal()
}

// Close all connections in the pool.
//
// Note: If a minimum number of open connections is configured along with a
// check time, the minimum number of connections will be reopened.
func (netpool *Netpool) Close() {
	for n := netpool.connections.Front(); n != nil; n = n.Next() {
		n.Value.(net.Conn).Close()
	}
}

// For metrics purposes, one can call actives to see any active connections
// both waiting and in-use.
func (netpool *Netpool) Actives() int {
	return int(netpool.actives)
}

// For metrics purposes, one can call count to see total count of created
// connections.
func (netpool *Netpool) Counter() int64 {
	return netpool.counter
}

// Size of the connection pool.
func (netpool *Netpool) Len() int {
	return netpool.connections.Len()
}
