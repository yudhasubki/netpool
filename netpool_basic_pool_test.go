package netpool

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewBasic(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := NewBasic(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, Config{
		MaxPool: 10,
		MinPool: 2,
	})
	if err != nil {
		t.Fatalf("failed to create basic pool: %v", err)
	}
	defer pool.Close()

	stats := pool.Stats()
	if stats.Idle < 2 {
		t.Errorf("expected at least 2 idle connections, got %d", stats.Idle)
	}
	if stats.MaxPool != 10 {
		t.Errorf("expected MaxPool 10, got %d", stats.MaxPool)
	}
}

func TestBasicGetPut(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := NewBasic(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, Config{
		MaxPool: 5,
		MinPool: 0,
	})
	if err != nil {
		t.Fatalf("failed to create request pool: %v", err)
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

func TestBasicConcurrent(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := NewBasic(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, Config{
		MaxPool: 100,
		MinPool: 0,
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := pool.Get()
			if err != nil {
				t.Errorf("Get() failed: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
			pool.Put(conn)
		}()
	}
	wg.Wait()

	stats := pool.Stats()
	if stats.InUse != 0 {
		t.Errorf("expected 0 in-use after concurrent test, got %d", stats.InUse)
	}
}

func TestBasicPoolClose(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, _ := NewBasic(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, Config{
		MaxPool: 5,
		MinPool: 2,
	})

	pool.Close()

	_, err := pool.Get()
	if err != ErrPoolClosed {
		t.Errorf("expected ErrPoolClosed, got %v", err)
	}
}

func TestBasicPutWithError(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, _ := NewBasic(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, Config{
		MaxPool: 5,
	})
	defer pool.Close()

	conn, _ := pool.Get()
	pool.PutWithError(conn, ErrInvalidConn)

	stats := pool.Stats()
	if stats.Active != 0 {
		t.Errorf("expected 0 active after PutWithError, got %d", stats.Active)
	}
}

func TestBasicContextCancellation(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, _ := NewBasic(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, Config{
		MaxPool: 1,
	})
	defer pool.Close()

	conn, _ := pool.Get()
	
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pool.GetWithContext(ctx)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	pool.Put(conn)
}

func TestBasicRace(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, _ := NewBasic(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, Config{
		MaxPool: 10,
	})
	defer pool.Close()

	var wg sync.WaitGroup
	var ops atomic.Int64

	for i := 0; i < 20; i++ {
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
	t.Logf("BasicPool Race: Completed %d operations", ops.Load())
}
