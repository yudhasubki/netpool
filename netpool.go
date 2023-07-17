package netpool

import (
	"container/list"
	"net"
	"sync"
)

type Netpooler interface {
	Close()
	Get() (net.Conn, error)
	Len() int
	Put(conn net.Conn, err error)
}

type Netpool struct {
	actives     int32
	cond        *sync.Cond
	connections *list.List
	fn          netpoolFunc
	mu          *sync.Mutex
	config      Config
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
		config:      config,
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
	}
	netpool.cond = sync.NewCond(netpool.mu)

	return netpool, nil
}

func (netpool *Netpool) Get() (net.Conn, error) {
	netpool.mu.Lock()
	defer netpool.mu.Unlock()

	for netpool.connections.Len() == 0 && netpool.actives >= netpool.config.MaxPool {
		netpool.cond.Wait()
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

	return c, nil
}

func (netpool *Netpool) Put(conn net.Conn, err error) {
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

	netpool.cond.Signal()
}

func (netpool *Netpool) Close() {
	for n := netpool.connections.Front(); n == nil; n = n.Next() {
		n.Value.(net.Conn).Close()
	}
}

func (netpool *Netpool) Len() int {
	return netpool.connections.Len()
}
