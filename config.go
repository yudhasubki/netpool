package netpool

import (
	"net"
)

type Config struct {
	DialHooks []func(c net.Conn) error
	MaxPool   int32
	MinPool   int32
}

type Opt func(c *Config)

func WithMaxPool(max int32) Opt {
	return func(c *Config) {
		c.MaxPool = max
	}
}

func WithMinPool(min int32) Opt {
	return func(c *Config) {
		c.MinPool = min
	}
}

func WithDialHooks(hooks ...func(c net.Conn) error) Opt {
	return func(c *Config) {
		c.DialHooks = append(c.DialHooks, hooks...)
	}
}
