package netpool

import "errors"

var (
	// ErrPoolClosed is returned when attempting to use a closed pool
	ErrPoolClosed = errors.New("netpool: pool is closed")

	// ErrInvalidConn is returned when a nil connection is provided
	ErrInvalidConn = errors.New("netpool: invalid connection")

	// ErrInvalidConfig is returned when pool configuration is invalid
	ErrInvalidConfig = errors.New("netpool: invalid configuration")

	// ErrConnReturned is returned when connection already returned
	ErrConnReturned = errors.New("connection already returned")

	// ErrDialTimeout is returned when dial operation exceeds the configured timeout
	ErrDialTimeout = errors.New("netpool: dial timeout exceeded")
)
