package netpool

import (
	"net"
	"time"
)

type Config struct {
	// DialHooks are optional callbacks executed immediately after a new
	// connection is created.
	//
	// If any hook returns an error, the connection is closed and discarded.
	// Hooks are only executed for newly created connections, not reused ones.
	DialHooks []func(c net.Conn) error

	// MaxPool is the maximum number of active connections allowed in the pool.
	//
	// When the pool reaches this limit, additional Get() calls will block
	// until a connection is returned or the context is canceled.
	MaxPool int32

	// MinPool is the minimum number of idle connections the pool will try
	// to maintain.
	//
	// Background maintenance ensures the pool does not shrink below this
	// number, even when idle connections are reaped.
	MinPool int32

	// MaxIdleTime is the maximum duration a connection may remain idle
	// in the pool before being closed.
	//
	// Idle connections exceeding this duration are removed automatically,
	// but the pool will never shrink below MinPool.
	//
	// A zero value disables idle connection reaping.
	MaxIdleTime time.Duration

	// MaintainerInterval controls how often the background maintainer
	// checks and replenishes the pool to satisfy MinPool.
	//
	// If zero, a sensible default is chosen based on MaxIdleTime.
	MaintainerInterval time.Duration

	// DialTimeout is the maximum duration allowed for creating a new connection.
	//
	// If the dial operation takes longer than this duration, it will be
	// canceled and an error will be returned.
	//
	// A zero value means no timeout (dial may block indefinitely).
	DialTimeout time.Duration
}

// Opt represents a functional option used to configure a Netpool.
type Opt func(c *Config)

// WithMaxPool sets the maximum number of active connections allowed in the pool.
func WithMaxPool(max int32) Opt {
	return func(c *Config) {
		c.MaxPool = max
	}
}

// WithMinPool sets the minimum number of idle connections the pool
// will attempt to maintain.
func WithMinPool(min int32) Opt {
	return func(c *Config) {
		c.MinPool = min
	}
}

// WithDialHooks registers one or more dial hooks to be executed
// after a connection is successfully created.
//
// Hooks are executed in the order provided. If any hook returns
// an error, the connection is closed and discarded.
func WithDialHooks(hooks ...func(c net.Conn) error) Opt {
	return func(c *Config) {
		c.DialHooks = append(c.DialHooks, hooks...)
	}
}

// WithMaxIdleTime sets the maximum idle duration for pooled connections.
//
// Connections that remain idle longer than this duration are automatically
// closed, provided the pool does not shrink below MinPool.
//
// A zero duration disables idle reaping.
func WithMaxIdleTime(d time.Duration) Opt {
	return func(c *Config) {
		c.MaxIdleTime = d
	}
}

// WithMaintainerInterval sets how frequently the background maintainer
// runs to ensure the pool satisfies MinPool.
//
// If not set, the interval is automatically derived from MaxIdleTime.
func WithMaintainerInterval(d time.Duration) Opt {
	return func(c *Config) {
		c.MaintainerInterval = d
	}
}

// WithDialTimeout sets the maximum duration for creating new connections.
//
// If a dial operation exceeds this timeout, it fails with a timeout error.
// A zero value disables the timeout.
func WithDialTimeout(d time.Duration) Opt {
	return func(c *Config) {
		c.DialTimeout = d
	}
}
