package netpool

import (
	"net"
	"time"
)

type config struct {
	DialHooks []func(c net.Conn) error
	MaxPool   int32
	MinPool   int32
	CheckTime time.Duration
	CullTime  time.Duration
}

type Opt func(c *config)

// Maximum number of connections to allow to be active at any given moment.
// Connections both in the pool and currently in use count towards this limit.
// Additional calls will be held in a locked state waiting for another active
// session to be put back into the pool.
//
// Warning: As the maximum number of connections is a hard limit, a circular
// locking state can be met if a calling function requests more than one open
// connection per triggered event and more than one event requests the maximum.
func WithMaxPool(max int32) Opt {
	return func(c *config) {
		c.MaxPool = max
	}
}

// Initial number of connections to open and then maintain over time.
// WithChecks must be set for a minimum number of connections to be opened when
// the minimum is unmet.
func WithMinPool(min int32) Opt {
	return func(c *config) {
		c.MinPool = min
	}
}

// Add a hook to the DialHooks stack, can be called more than once, for
// handling connection setup steps, like a login.  All the dial hooks must pass
// per connection or an error will be returned with the Get() call with the
// error from the hook.
func WithDialHooks(hooks ...func(c net.Conn) error) Opt {
	return func(c *config) {
		c.DialHooks = append(c.DialHooks, hooks...)
	}
}

// Upon every check time, d, one connection will verified and then put back in
// the pool if the check passed.
func WithChecks(d time.Duration) Opt {
	return func(c *config) {
		c.CheckTime = d
	}
}

// Upon the check interval, close connections in pool if no Get() or Put()
// calls have been seen in the given timeout.
func WithTimeout(d time.Duration) Opt {
	return func(c *config) {
		c.CullTime = d
	}
}
