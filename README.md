# Netpool - Go TCP Connection Pool

## Description

Netpool is a lightweight and efficient TCP connection pool library for Golang. It provides a simple way to manage and reuse TCP connections, reducing the overhead of establishing new connections for each request and improving performance in high-concurrency scenarios.

## Features
- TCP connection pooling for efficient connection reuse
- Configurable maximum connection limit per host
- Automatic connection reaping to remove idle connections
- Graceful handling of connection errors and reconnecting
- Customizable connection dialer for flexible connection establishment
Thread-safe operations for concurrent use

## Installation
```
go get github.com/yudhasubki/netpool
```

## Quick Start	

### Basic Usage
```go
package main

import (
    "log"
    "net"
    
    "github.com/yudhasubki/netpool"
)

func main() {
    // Create a pool
    pool, err := netpool.New(func() (net.Conn, error) {
		return net.Dial("tcp", "localhost:6379")
	},
		netpool.WithMinPool(5),
		netpool.WithMaxPool(20),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	conn, err := pool.Get()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close() // Automatically returns to pool

	conn.Write([]byte("PING\r\n"))
}
```

### With Idle Timeout
```go
pool, err := netpool.New(func() (net.Conn, error) {
    return net.Dial("tcp", "localhost:6379")
},
    netpool.WithMinPool(2),
    netpool.WithMaxPool(10),
    netpool.WithMaxIdleTime(30*time.Second), 
)
```

### Monitoring Pool Statistics
```go
stats := pool.Stats()
fmt.Printf("Active: %d, Idle: %d, InUse: %d\n",
	stats.Active, stats.Idle, stats.InUse)
```

## How it works
### Connection Lifecycle
- Creation: Connections are created using the provided factory function
- Dial Hooks: Optional hooks run for connection initialization
- Pool Storage: Idle connections are stored in a FIFO queue
- Health Check: Connections are validated before being returned (if configured)
- Idle Timeout: Unused connections are automatically cleaned up (if configured)
- Auto-Return: Connections automatically return to pool on Close()

### Auto-Return Connection Wrapper
Connections returned by Get() are automatically wrapped in a pooledConn that returns the connection to the pool when Close() is called. This eliminates the need for manual Put() calls:
```go
// Old way (manual Put)
conn, _ := pool.Get()
defer pool.Put(conn, nil)

// New way (auto-return)
conn, _ := pool.Get()
defer conn.Close() // Automatically returns to pool
```

If a connection encounters an error and should not be reused, use MarkUnusable():

```go
goconn, _ := pool.Get()

_, err := conn.Write(data)
if err != nil {
    if pc, ok := conn.(interface{ MarkUnusable() error }); ok {
        pc.MarkUnusable() // Connection won't be returned to pool
    }
    return err
}

conn.Close() // Normal return to pool
```

### Thread Safety
netpool uses fine-grained locking to minimize contention:

Pool operations are protected by a mutex
Condition variables handle blocking when pool is exhausted
Connection health checks run without holding the main lock

## Contributing
Contributions are welcome! If you find a bug or have a suggestion for improvement, please open an issue or submit a pull request. Make sure to follow the existing coding style and write tests for any new functionality.

## License
This project is licensed under the MIT License. See the LICENSE file for details.

## Acknowledgments
NetPool is inspired by various connection pool implementations in the Golang ecosystem. We would like to thank the authors of those projects for their contributions and ideas.