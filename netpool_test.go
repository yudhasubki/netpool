package netpool

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func createTestServer(t testing.TB) (net.Listener, string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					c.Write(buf[:n])
				}
			}(conn)
		}
	}()

	return listener, listener.Addr().String()
}

func TestNewPool(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 10,
		MinPool: 2,
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	stats := pool.Stats()
	if stats.Idle < 2 {
		t.Errorf("expected at least 2 idle connections, got %d", stats.Idle)
	}
}

func TestGetPut(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 5,
		MinPool: 0,
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	// Get a connection
	conn, err := pool.Get()
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}

	stats := pool.Stats()
	if stats.InUse != 1 {
		t.Errorf("expected 1 in-use, got %d", stats.InUse)
	}

	// Put it back
	pool.Put(conn)

	stats = pool.Stats()
	if stats.InUse != 0 {
		t.Errorf("expected 0 in-use after Put, got %d", stats.InUse)
	}
	if stats.Idle != 1 {
		t.Errorf("expected 1 idle after Put, got %d", stats.Idle)
	}
}

func TestConcurrentAccess(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 10,
		MinPool: 0,
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := pool.Get()
			if err != nil {
				t.Errorf("Get() failed: %v", err)
				return
			}
			time.Sleep(1 * time.Millisecond)
			pool.Put(conn)
		}()
	}
	wg.Wait()

	stats := pool.Stats()
	if stats.InUse != 0 {
		t.Errorf("expected 0 in-use after concurrent test, got %d", stats.InUse)
	}
}

func TestMaxPoolLimit(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 3,
		MinPool: 0,
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	// Get all 3 connections
	conns := make([]net.Conn, 3)
	for i := 0; i < 3; i++ {
		conn, err := pool.Get()
		if err != nil {
			t.Fatalf("Get() %d failed: %v", i, err)
		}
		conns[i] = conn
	}

	stats := pool.Stats()
	if stats.Active != 3 {
		t.Errorf("expected 3 active, got %d", stats.Active)
	}

	// Try to get 4th with timeout - should fail
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = pool.GetWithContext(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}

	// Return connections
	for _, conn := range conns {
		pool.Put(conn)
	}
}

func TestPoolClose(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 5,
		MinPool: 2,
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	pool.Close()

	// Get after close should fail
	_, err = pool.Get()
	if err != ErrPoolClosed {
		t.Errorf("expected ErrPoolClosed, got %v", err)
	}
}

func TestPutWithError(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 5,
		MinPool: 0,
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	conn, _ := pool.Get()

	// Put with error should close the connection
	pool.PutWithError(conn, ErrInvalidConn)

	stats := pool.Stats()
	if stats.Active != 0 {
		t.Errorf("expected 0 active after PutWithError, got %d", stats.Active)
	}
}

func TestDialFailure(t *testing.T) {
	callCount := 0
	_, err := New(func(ctx context.Context) (net.Conn, error) {
		callCount++
		return nil, net.UnknownNetworkError("test error")
	}, Config{
		MaxPool: 5,
		MinPool: 2,
	})

	if err == nil {
		t.Error("expected error from New with failing dial")
	}
}

func TestContextCancellation(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, _ := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 1,
		MinPool: 0,
	})
	defer pool.Close()

	// Hold the only connection
	conn, _ := pool.Get()

	// Try to get with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pool.GetWithContext(ctx)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	pool.Put(conn)
}

func TestDialTimeout(t *testing.T) {
	// Use a non-routable IP to simulate timeout
	pool, err := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", "10.255.255.1:12345")
	}, Config{
		MaxPool:     5,
		MinPool:     0,
		DialTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	_, err = pool.Get()
	if err.Error() != "dial tcp 10.255.255.1:12345: i/o timeout" {
		t.Errorf("expected ErrDialTimeout, got %v", err)
	}
}

// Benchmark

func BenchmarkPoolGet(b *testing.B) {
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

func BenchmarkPoolConcurrent(b *testing.B) {
	listener, addr := createTestServer(b)
	defer listener.Close()

	pool, _ := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 300,
		MinPool: 10,
	})
	defer pool.Close()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := pool.Get()
			if err != nil {
				b.Fatal(err)
			}
			conn.Write([]byte("test data"))
			pool.Put(conn)
		}
	})
}

func BenchmarkPoolGetNoContention(b *testing.B) {
	listener, addr := createTestServer(b)
	defer listener.Close()

	pool, _ := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 1000,
		MinPool: 100,
	})
	defer pool.Close()

	// Wait for MinPool to be ready
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := pool.Get()
		if err != nil {
			b.Fatal(err)
		}
		pool.Put(conn)
	}
}

// Race condition tests

func TestRaceGetPut(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, _ := New(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, Config{
		MaxPool: 10,
		MinPool: 0,
	})
	defer pool.Close()

	var wg sync.WaitGroup
	var ops atomic.Int64

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				conn, err := pool.Get()
				if err != nil {
					return
				}
				ops.Add(1)
				pool.Put(conn)
			}
		}()
	}

	wg.Wait()
	t.Logf("Completed %d operations", ops.Load())
}
