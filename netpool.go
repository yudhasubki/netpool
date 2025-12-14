package netpool

import (
	"container/list"
	"errors"
	"net"
	"sync"
)

type Netpooler interface {
	Close()
	Get() (net.Conn, error)
	Len() int
	Put(conn net.Conn, err error)
	Stats() PoolStats
}

type Netpool struct {
	actives     int32
	cond        *sync.Cond
	connections *list.List
	allConns    map[net.Conn]bool
	fn          netpoolFunc
	mu          *sync.Mutex
	config      Config
	closed      bool
}

type PoolStats struct {
	TotalCreated int
	Idle         int
	InUse        int
	MaxPool      int32
	MinPool      int32
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

	netpool := &Netpool{
		fn:          fn,
		mu:          new(sync.Mutex),
		actives:     0,
		connections: list.New(),
		allConns:    make(map[net.Conn]bool),
		config:      config,
		closed:      false,
	}

	netpool.cond = sync.NewCond(netpool.mu)

	for i := int32(0); i < config.MinPool; i++ {
		conn, err := netpool.fn()
		if err != nil {
			netpool.Close()
			return nil, err
		}

		if len(netpool.config.DialHooks) > 0 {
			for _, dialHook := range netpool.config.DialHooks {
				err = dialHook(conn)
				if err != nil {
					conn.Close()
					netpool.Close()
					return nil, err
				}
			}
		}

		netpool.connections.PushBack(conn)
		netpool.allConns[conn] = true
		netpool.actives++
	}

	return netpool, nil
}

func (netpool *Netpool) Get() (net.Conn, error) {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	if netpool.closed {
		return nil, errors.New("pool is closed")
	}

	for netpool.connections.Len() == 0 && netpool.actives >= netpool.config.MaxPool {
		netpool.cond.Wait()
		if netpool.closed {
			return nil, errors.New("pool is closed")
		}
	}

	if netpool.connections.Len() > 0 {
		return netpool.connections.Remove(netpool.connections.Front()).(net.Conn), nil
	}

	c, err := netpool.fn()
	if err != nil {
		return nil, err
	}

	if len(netpool.config.DialHooks) > 0 {
		for _, hook := range netpool.config.DialHooks {
			err = hook(c)
			if err != nil {
				c.Close()

				return nil, err
			}
		}
	}

	netpool.actives++
	netpool.allConns[c] = true

	return c, nil
}

func (netpool *Netpool) Put(conn net.Conn, err error) {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	if netpool.closed {
		if conn != nil {
			conn.Close()
			delete(netpool.allConns, conn)
		}
		return
	}

	if err != nil || conn == nil {
		if conn != nil {
			conn.Close()
			delete(netpool.allConns, conn)
		}
		netpool.actives--
		netpool.cond.Signal()
		return
	}

	if netpool.connections.Len() >= int(netpool.config.MaxPool) {
		conn.Close()
		delete(netpool.allConns, conn)
		netpool.actives--
	} else {
		netpool.connections.PushBack(conn)
	}
	netpool.cond.Signal()
}

func (netpool *Netpool) Close() {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	if netpool.closed {
		return
	}
	netpool.closed = true

	for n := netpool.connections.Front(); n != nil; n = n.Next() {
		n.Value.(net.Conn).Close()
	}

	// clear list
	netpool.connections.Init()

	for conn := range netpool.allConns {
		conn.Close()
	}
	netpool.allConns = make(map[net.Conn]bool)

	netpool.actives = 0
	netpool.cond.Broadcast()
}

func (netpool *Netpool) Stats() PoolStats {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	idle := netpool.connections.Len()
	total := int(netpool.actives)

	return PoolStats{
		TotalCreated: total,
		Idle:         idle,
		InUse:        total - idle,
		MaxPool:      netpool.config.MaxPool,
		MinPool:      netpool.config.MinPool,
	}
}

func (netpool *Netpool) Len() int {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()
	return netpool.connections.Len()
}
