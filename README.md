# Netpool - Lock-Free Go TCP Connection Pool

[![Go Reference](https://pkg.go.dev/badge/github.com/yudhasubki/netpool.svg)](https://pkg.go.dev/github.com/yudhasubki/netpool)

## 🚀 Performance

Netpool is the Go connection pool. It uses a lock-free channel design with zero-allocation pass-by-value optimization.

| Library | ns/op | Throughput | Memory | Allocations | Features |
|---------|-------|-----------|--------|-------------|----------|
| **netpool (Basic)** | **42 ns** | **23.8M ops/sec** | **0 B** | **0 allocs** | Maximum Speed (No Idle/Health) |
| **netpool (Standard)**| **118 ns** | **8.4M ops/sec** | **0 B** | **0 allocs** | IdleTimeout, HealthCheck |
| fatih/pool | 124 ns | 8.0M ops/sec | 64 B | 1 alloc | No HealthCheck |
| silenceper/pool | 303 ns | 3.3M ops/sec | 48 B | 1 alloc | IdleTimeout, HealthCheck |

## Features

- **Lock-free** - Uses channels and atomics only
- **Zero allocation** - No memory allocations on Get/Put
- **Idle Timeout** - Automatically closes stale connections
- **Health Check** - Validates connections before use
- **Thread-safe** - Safe for concurrent use

## Installation

```bash
go get github.com/yudhasubki/netpool
```

## Quick Start

### Standard Pool (Recommended)
Best balance of features and performance (118 ns/op).

```go
package main

import (
    "log"
    "net"
    "time"
    
    "github.com/yudhasubki/netpool"
)

func main() {
    pool, err := netpool.New(func() (net.Conn, error) {
        return net.Dial("tcp", "localhost:6379")
    }, netpool.Config{
        MaxPool:     100,
        MinPool:     10,
        MaxIdleTime: 30 * time.Second, // Optional
        HealthCheck: func(conn net.Conn) error { // Optional
            return nil
        },
    })
    
    conn, _ := pool.Get()
    defer pool.Put(conn)
}
```

### Basic Pool (Maximum Performance)
For use cases needing absolute raw speed (~40 ns/op).
**Note:** Does NOT support `MaxIdleTime` or `HealthCheck`.

```go
// Use NewBasic() instead of New()
pool, err := netpool.NewBasic(func() (net.Conn, error) {
    return net.Dial("tcp", "localhost:6379")
}, netpool.Config{
    MaxPool: 100,
    MinPool: 10,
})

conn, _ := pool.Get()
defer pool.Put(conn)
```

## API

### Creating a Pool

```go
pool, err := netpool.New(factory, netpool.Config{
    MaxPool:     100,              // Maximum connections
    MinPool:     10,               // Minimum idle connections
    DialTimeout: 5 * time.Second,  // Connection creation timeout
    MaxIdleTime: 30 * time.Second, // Close connections idle too long
    HealthCheck: pingFunc,         // Validate connection on Get()
})
```

### Getting a Connection

```go
// Simple get (blocks if pool is full)
conn, err := pool.Get()

// Get with context (supports cancellation/timeout)
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
conn, err := pool.GetWithContext(ctx)
```

### Returning a Connection

```go
// Return healthy connection
pool.Put(conn)

// Return with error (connection will be closed)
pool.PutWithError(conn, err)
```

### Pool Statistics

```go
stats := pool.Stats()
fmt.Printf("Active: %d, Idle: %d, InUse: %d\n",
    stats.Active, stats.Idle, stats.InUse)
```

## How It Works

Netpool uses a **Pass-by-Value** channel design for maximum efficiency:

1. **Wait-Free Path**: `Get()` and `Put()` operations use Go channels with value copying.
2. **Zero Allocation**: Connection wrappers (`idleConn`) are passed by value (copying ~40 bytes), eliminating `sync.Pool` overhead and heap allocations.
3. **Atomic State**: Pool size is tracked with `atomic.Int32` for contention-free reads.

This design beats standard `sync.Pool` or mutex-based implementations by reducing memory pressure and CPU cycles.

## Running Benchmarks

```bash
# Run comparison with other libraries
go test -bench=BenchmarkComparison -benchmem ./...
```

## Credits

This project is inspired by the design and implementation of:

- [fatih/pool](https://github.com/fatih/pool)
- [silenceper/pool](https://github.com/silenceper/pool)

We thank the authors for their contributions to the Go ecosystem.

## License

MIT License - see [LICENSE](LICENSE) file.