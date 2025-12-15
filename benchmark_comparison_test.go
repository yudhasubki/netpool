package netpool

import (
	"context"
	"net"
	"testing"

	fatihpool "github.com/fatih/pool"
	silencerpool "github.com/silenceper/pool"
)

func BenchmarkComparisonNetpool(b *testing.B) {
	listener, addr := createTestServer(b)
	defer listener.Close()

	pool, _ := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 100,
		MinPool: 50,
	})
	defer pool.Close()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := pool.Get()
			if err != nil {
				b.Fatal(err)
			}
			pool.Put(conn)
		}
	})
}

func BenchmarkComparisonFatihPool(b *testing.B) {
	listener, addr := createTestServer(b)
	defer listener.Close()

	factory := func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}

	pool, err := fatihpool.NewChannelPool(50, 100, factory)
	if err != nil {
		b.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := pool.Get()
			if err != nil {
				b.Fatal(err)
			}
			conn.Close()
		}
	})
}

func BenchmarkComparisonSilencerPool(b *testing.B) {
	listener, addr := createTestServer(b)
	defer listener.Close()

	poolConfig := &silencerpool.Config{
		InitialCap: 10,
		MaxIdle:    50,
		MaxCap:     100,
		Factory: func() (interface{}, error) {
			return net.Dial("tcp", addr)
		},
		Close: func(v interface{}) error {
			return v.(net.Conn).Close()
		},
	}

	pool, err := silencerpool.NewChannelPool(poolConfig)
	if err != nil {
		b.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Release()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := pool.Get()
			if err != nil {
				b.Fatal(err)
			}
			pool.Put(conn)
		}
	})
}

func BenchmarkComparisonBasicPool(b *testing.B) {
	listener, addr := createTestServer(b)
	defer listener.Close()

	pool, _ := NewBasic(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 100,
		MinPool: 50,
	})
	defer pool.Close()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := pool.Get()
			if err != nil {
				b.Fatal(err)
			}
			pool.Put(conn)
		}
	})
}
