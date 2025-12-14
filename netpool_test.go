package netpool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

func createTestServer(t *testing.T) (net.Listener, string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	// Accept connections in background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Echo server: read and write back
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

func TestNewConnection(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(3), WithMaxPool(10))

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	defer pool.Close()

	if pool.Len() != 3 {
		t.Errorf("expected 3 idle connections, got %d", pool.Len())
	}

	stats := pool.Stats()
	if stats.Active != 3 {
		t.Errorf("expected 3 total connections, got %d", stats.Active)
	}
}

func TestGetConnection(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(2), WithMaxPool(5))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	defer conn.Close() // Auto-return to pool

	testMsg := []byte("hello")
	_, err = conn.Write(testMsg)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if string(buf[:n]) != string(testMsg) {
		t.Errorf("expected %s, got %s", testMsg, buf[:n])
	}

	// Connection auto-returns on Close()
	conn.Close()

	// Give a moment for Close() to complete
	time.Sleep(10 * time.Millisecond)

	if pool.Len() != 2 {
		t.Errorf("expected 2 idle connections after Close, got %d", pool.Len())
	}
}

func TestGetCreatesNewConnection(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(1), WithMaxPool(5))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	conn1, _ := pool.Get()
	defer conn1.Close()

	conn2, err := pool.Get()
	if err != nil {
		t.Fatalf("expected new connection, got error: %v", err)
	}
	defer conn2.Close()

	stats := pool.Stats()
	if stats.Active != 2 {
		t.Errorf("expected 2 total connections, got %d", stats.Active)
	}
	if stats.InUse != 2 {
		t.Errorf("expected 2 in-use connections, got %d", stats.InUse)
	}
}

func TestPutWithConnectionError(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(1), WithMaxPool(5))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	conn, _ := pool.Get()
	initialStats := pool.Stats()

	// Use MarkUnusable for better error handling
	if pc, ok := conn.(interface{ MarkUnusable() error }); ok {
		err := pc.MarkUnusable()
		if err != nil {
			t.Fatalf("MarkUnusable failed: %v", err)
		}
	} else {
		t.Fatal("connection doesn't support MarkUnusable")
	}

	// Give time for cleanup
	time.Sleep(50 * time.Millisecond)

	finalStats := pool.Stats()

	if finalStats.Active != initialStats.Active-1 {
		t.Errorf("expected TotalCreated to decrease from %d to %d, got %d",
			initialStats.Active, initialStats.Active-1, finalStats.Active)
	}
}

func TestGetBlocksAtMaxPool(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(1), WithMaxPool(2))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	conn1, _ := pool.Get()
	conn2, _ := pool.Get()

	blocked := make(chan bool, 1)
	var conn3 net.Conn

	go func() {
		conn3, _ = pool.Get()
		blocked <- true
	}()

	select {
	case <-blocked:
		t.Fatal("Get() should have blocked")
	case <-time.After(100 * time.Millisecond):
	}

	conn1.Close() // Auto-return to pool

	select {
	case <-blocked:
		if conn3 == nil {
			t.Fatal("expected connection after unblock")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Get() didn't unblock")
	}

	conn2.Close()
	conn3.Close()
}

func TestConcurrentGetPutConnections(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(2), WithMaxPool(10))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	var wg sync.WaitGroup
	goroutines := 20
	iterations := 50

	errorsChan := make(chan error, goroutines*iterations)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				conn, err := pool.Get()
				if err != nil {
					errorsChan <- err
					continue
				}

				// Use the connection
				msg := []byte("test")
				_, err = conn.Write(msg)
				if err != nil {
					// Mark unusable on error
					if pc, ok := conn.(interface{ MarkUnusable() error }); ok {
						pc.MarkUnusable()
					} else {
						conn.Close()
					}
					errorsChan <- err
					continue
				}

				buf := make([]byte, 1024)
				conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				_, err = conn.Read(buf)

				if err != nil {
					// Mark unusable on error
					if pc, ok := conn.(interface{ MarkUnusable() error }); ok {
						pc.MarkUnusable()
					} else {
						conn.Close()
					}
				} else {
					conn.Close() // Normal return to pool
				}
			}
		}(i)
	}

	wg.Wait()
	close(errorsChan)

	errorCount := 0
	for err := range errorsChan {
		t.Logf("error during test: %v", err)
		errorCount++
	}

	if errorCount > 0 {
		t.Errorf("got %d errors during concurrent operations", errorCount)
	}

	stats := pool.Stats()
	t.Logf("Final stats - Total: %d, Idle: %d, InUse: %d",
		stats.Active, stats.Idle, stats.InUse)
}

func TestCloseConnections(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(3), WithMaxPool(5))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	conns := make([]net.Conn, 2)
	for i := 0; i < 2; i++ {
		conns[i], _ = pool.Get()
	}

	pool.Close()

	stats := pool.Stats()
	if stats.Idle != 0 {
		t.Errorf("expected 0 idle connections after Close, got %d", stats.Idle)
	}

	// Connections should be closed by pool.Close()
	_, err = conns[0].Write([]byte("test"))
	if err == nil {
		t.Error("expected error writing to closed connection")
	}
}

func TestNewDialFailure(t *testing.T) {
	callCount := 0
	_, err := New(func() (net.Conn, error) {
		callCount++
		if callCount == 2 {
			return nil, errors.New("dial failed")
		}
		return net.Dial("tcp", "127.0.0.1:0")
	}, WithMinPool(3))

	if err == nil {
		t.Fatal("expected error from failed dial")
	}
}

func TestDialHooksConnection(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	hookCalled := 0
	hook := func(conn net.Conn) error { // Updated signature: any instead of net.Conn
		hookCalled++
		c := conn.(net.Conn)
		// Test the connection
		_, err := c.Write([]byte("hook test"))
		return err
	}

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(2), WithDialHooks(hook)) // Pass as slice

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	if hookCalled != 2 {
		t.Errorf("expected hook to be called 2 times during init, got %d", hookCalled)
	}

	conn1, _ := pool.Get()
	conn2, _ := pool.Get()

	if hookCalled != 2 {
		t.Errorf("expected hook still 2 times (reusing pool connections), got %d", hookCalled)
	}

	conn3, _ := pool.Get()

	if hookCalled != 3 {
		t.Errorf("expected hook to be called 3 times total (created new conn), got %d", hookCalled)
	}

	conn1.Close()
	conn2.Close()
	conn3.Close()
}

func TestPooledConnAutoReturn(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(2), WithMaxPool(5))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	initialStats := pool.Stats()

	// Get connection
	conn, err := pool.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	afterGetStats := pool.Stats()
	if afterGetStats.InUse != initialStats.InUse+1 {
		t.Errorf("expected InUse to increase by 1")
	}

	err = conn.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	afterCloseStats := pool.Stats()
	if afterCloseStats.InUse != initialStats.InUse {
		t.Errorf("expected InUse to return to original after Close")
	}

	_, err = conn.Write([]byte("test"))
	if err == nil {
		t.Error("expected error writing to closed connection")
	}
}

func TestPooledConnMarkUnusable(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(2), WithMaxPool(5))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	initialStats := pool.Stats()

	conn, _ := pool.Get()

	if pc, ok := conn.(interface{ MarkUnusable() error }); ok {
		err = pc.MarkUnusable()
		if err != nil {
			t.Fatalf("MarkUnusable failed: %v", err)
		}
	} else {
		t.Fatal("connection doesn't implement MarkUnusable")
	}

	finalStats := pool.Stats()
	if finalStats.Active != initialStats.Active-1 {
		t.Errorf("expected TotalCreated to decrease by 1 after MarkUnusable")
	}
}

func TestIdleTimeoutWithPoolGrowthAndShrink(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	},
		WithMinPool(2),
		WithMaxPool(10),
		WithMaxIdleTime(2*time.Second), // 2 second idle timeout
	)

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	t.Log("Phase 1: Initial state")
	stats := pool.Stats()
	if stats.Active != 2 {
		t.Errorf("expected 2 initial connections, got %d", stats.Active)
	}
	t.Logf("Initial - Total: %d, Idle: %d, InUse: %d", stats.Active, stats.Idle, stats.InUse)

	// Phase 2: Grow pool to max
	t.Log("Phase 2: Growing pool to max capacity")
	conns := make([]net.Conn, 10)
	for i := 0; i < 10; i++ {
		conns[i], err = pool.Get()
		if err != nil {
			t.Fatalf("failed to get connection %d: %v", i, err)
		}
	}

	stats = pool.Stats()
	if stats.Active != 10 {
		t.Errorf("expected 10 connections at max, got %d", stats.Active)
	}
	if stats.InUse != 10 {
		t.Errorf("expected 10 in-use connections, got %d", stats.InUse)
	}
	t.Logf("At max capacity - Total: %d, Idle: %d, InUse: %d", stats.Active, stats.Idle, stats.InUse)

	// Phase 3: Return all connections to pool
	t.Log("Phase 3: Returning all connections to pool")
	for i := 0; i < 10; i++ {
		conns[i].Close()
	}
	time.Sleep(100 * time.Millisecond) // Wait for close to complete

	stats = pool.Stats()
	if stats.Idle != 10 {
		t.Errorf("expected 10 idle connections, got %d", stats.Idle)
	}
	if stats.InUse != 0 {
		t.Errorf("expected 0 in-use connections, got %d", stats.InUse)
	}
	t.Logf("All returned - Total: %d, Idle: %d, InUse: %d", stats.Active, stats.Idle, stats.InUse)

	// Phase 4: Wait for idle timeout to kick in (should clean up to MinPool)
	t.Log("Phase 4: Waiting for idle timeout...")
	time.Sleep(3 * time.Second) // Wait longer than MaxIdleTime

	stats = pool.Stats()
	t.Logf("After idle timeout - Total: %d, Idle: %d, InUse: %d", stats.Active, stats.Idle, stats.InUse)

	// Should have cleaned up idle connections but kept at least MinPool
	if stats.Active < 2 {
		t.Errorf("pool went below MinPool: got %d, expected at least 2", stats.Active)
	}
	if stats.Active > 5 {
		t.Errorf("idle reaper didn't clean up enough: got %d, expected <= 5", stats.Active)
	}

	// Phase 5: Verify pool still works after cleanup
	t.Log("Phase 5: Verifying pool still works after cleanup")
	testConn, err := pool.Get()
	if err != nil {
		t.Fatalf("pool not working after idle cleanup: %v", err)
	}

	// Use the connection
	testMsg := []byte("test after cleanup")
	_, err = testConn.Write(testMsg)
	if err != nil {
		t.Fatalf("connection not working after cleanup: %v", err)
	}

	buf := make([]byte, 1024)
	testConn.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, err := testConn.Read(buf)
	if err != nil {
		t.Fatalf("read failed after cleanup: %v", err)
	}

	if string(buf[:n]) != string(testMsg) {
		t.Errorf("echo failed: expected %s, got %s", testMsg, buf[:n])
	}

	testConn.Close()
	t.Log("Phase 5: Pool still functional")

	// Phase 6: Grow again and verify it works
	t.Log("Phase 6: Growing pool again after cleanup")
	newConns := make([]net.Conn, 5)
	for i := 0; i < 5; i++ {
		newConns[i], err = pool.Get()
		if err != nil {
			t.Fatalf("failed to get connection after cleanup: %v", err)
		}
	}

	stats = pool.Stats()
	t.Logf("After regrowth - Total: %d, Idle: %d, InUse: %d", stats.Active, stats.Idle, stats.InUse)

	if stats.InUse < 5 {
		t.Errorf("expected at least 5 in-use connections, got %d", stats.InUse)
	}

	// Cleanup
	for i := 0; i < 5; i++ {
		newConns[i].Close()
	}

	t.Log("Test completed successfully")
}

func TestRepeatedGrowthShrinkDoesNotLeak(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(
		func() (net.Conn, error) {
			return net.Dial("tcp", addr)
		},
		WithMinPool(2),
		WithMaxPool(20),
		WithMaxIdleTime(200*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	for i := 0; i < 10; i++ {
		conns := make([]net.Conn, 20)
		for j := 0; j < 20; j++ {
			conns[j], _ = pool.Get()
		}

		for j := 0; j < 20; j++ {
			conns[j].Close()
		}

		time.Sleep(400 * time.Millisecond)

		stats := pool.Stats()
		if stats.Active > 5 {
			t.Fatalf("iteration %d: pool not shrinking properly: %+v", i, stats)
		}
	}
}

func TestIdleReaperWhileConcurrentGet(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(
		func() (net.Conn, error) {
			return net.Dial("tcp", addr)
		},
		WithMinPool(1),
		WithMaxPool(5),
		WithMaxIdleTime(200*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				conn, err := pool.Get()
				if err == nil {
					time.Sleep(10 * time.Millisecond)
					conn.Close()
				}
			}
		}
	}()

	time.Sleep(1 * time.Second) // let reaper & maintainer fight

	close(stop)

	stats := pool.Stats()
	if stats.Active < 1 {
		t.Fatalf("pool corrupted: %+v", stats)
	}
}

func BenchmarkPoolGet(b *testing.B) {
	listener, addr := createBenchmarkTestServer(b)
	defer listener.Close()

	pool, _ := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(10), WithMaxPool(300))
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

func TestContextCancelDuringWait(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(0), WithMaxPool(1))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	// Exhaust the pool
	conn1, err := pool.Get()
	if err != nil {
		t.Fatalf("failed to get first connection: %v", err)
	}
	defer conn1.Close()

	// Verify pool is exhausted
	stats := pool.Stats()
	if stats.InUse != 1 || stats.Idle != 0 {
		t.Errorf("pool should be exhausted: InUse=%d, Idle=%d", stats.InUse, stats.Idle)
	}

	// Try to get with timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = pool.GetWithContext(ctx)
	elapsed := time.Since(start)

	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}

	// Should timeout around 100ms, not instantly
	if elapsed < 50*time.Millisecond {
		t.Errorf("timeout happened too quickly: %v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("timeout took too long: %v", elapsed)
	}

	t.Logf("Context cancellation detected after %v", elapsed)
}

func TestCloseWithWaiters(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(0), WithMaxPool(1))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	// Exhaust the pool
	conn1, err := pool.Get()
	if err != nil {
		t.Fatalf("failed to get connection: %v", err)
	}

	// Start goroutine that will block waiting for a connection
	done := make(chan error, 1)
	started := make(chan struct{})

	go func() {
		close(started) // Signal that goroutine started
		_, err := pool.Get()
		done <- err
	}()

	// Wait for goroutine to start and begin waiting
	<-started
	time.Sleep(50 * time.Millisecond)

	// Close pool while goroutine is waiting
	pool.Close()

	// The waiting goroutine should unblock with ErrPoolClosed
	select {
	case err := <-done:
		if err != ErrPoolClosed {
			t.Errorf("expected ErrPoolClosed, got %v", err)
		}
		t.Logf("Waiter properly unblocked with: %v", err)
	case <-time.After(1 * time.Second):
		t.Fatal("waiting goroutine did not unblock after pool.Close()")
	}

	// Verify connection is closed
	_, err = conn1.Write([]byte("test"))
	if err == nil {
		t.Error("connection should be closed after pool.Close()")
	}

	// Subsequent Get should return ErrPoolClosed
	_, err = pool.Get()
	if err != ErrPoolClosed {
		t.Errorf("Get after Close should return ErrPoolClosed, got %v", err)
	}
}

func TestStatsConsistency(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(2), WithMaxPool(10))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	var wg sync.WaitGroup
	errors := make(chan string, 100)

	// Goroutines that continuously check stats consistency
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				stats := pool.Stats()

				// Active should always equal Idle + InUse
				if stats.Active != stats.Idle+stats.InUse {
					errors <- fmt.Sprintf("goroutine %d: inconsistent stats: Active=%d, Idle=%d, InUse=%d",
						id, stats.Active, stats.Idle, stats.InUse)
				}

				// Active should never exceed MaxPool
				if stats.Active > int(stats.MaxPool) {
					errors <- fmt.Sprintf("goroutine %d: Active(%d) > MaxPool(%d)",
						id, stats.Active, stats.MaxPool)
				}

				// Idle should never exceed Active
				if stats.Idle > stats.Active {
					errors <- fmt.Sprintf("goroutine %d: Idle(%d) > Active(%d)",
						id, stats.Idle, stats.Active)
				}

				// InUse should never exceed Active
				if stats.InUse > stats.Active {
					errors <- fmt.Sprintf("goroutine %d: InUse(%d) > Active(%d)",
						id, stats.InUse, stats.Active)
				}

				time.Sleep(1 * time.Millisecond)
			}
		}(i)
	}

	// Goroutines that Get and Put connections
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				conn, err := pool.Get()
				if err != nil {
					continue
				}

				// Use the connection briefly
				time.Sleep(5 * time.Millisecond)

				// Randomly mark some as unusable
				if j%10 == 0 {
					if pc, ok := conn.(interface{ MarkUnusable() error }); ok {
						pc.MarkUnusable()
					}
				} else {
					conn.Close()
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for any consistency errors
	errorCount := 0
	for errMsg := range errors {
		t.Error(errMsg)
		errorCount++
		if errorCount >= 10 {
			t.Log("... (more errors suppressed)")
			break
		}
	}

	if errorCount > 0 {
		t.Fatalf("found %d stats consistency violations", errorCount)
	}

	// Final verification
	finalStats := pool.Stats()
	t.Logf("Final stats: Active=%d, Idle=%d, InUse=%d, MaxPool=%d, MinPool=%d",
		finalStats.Active, finalStats.Idle, finalStats.InUse, finalStats.MaxPool, finalStats.MinPool)

	if finalStats.Active != finalStats.Idle+finalStats.InUse {
		t.Errorf("final stats inconsistent: Active=%d != Idle(%d) + InUse(%d)",
			finalStats.Active, finalStats.Idle, finalStats.InUse)
	}
}

func TestMultipleWaitersUnblockOnClose(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(0), WithMaxPool(1))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	// Exhaust the pool
	conn1, err := pool.Get()
	if err != nil {
		t.Fatalf("failed to get connection: %v", err)
	}

	// Start multiple goroutines waiting for connections
	numWaiters := 5
	done := make(chan error, numWaiters)

	for i := 0; i < numWaiters; i++ {
		go func(id int) {
			_, err := pool.Get()
			done <- err
		}(i)
	}

	// Give them time to start waiting
	time.Sleep(100 * time.Millisecond)

	// Close the pool
	pool.Close()

	// All waiters should unblock with ErrPoolClosed
	timeout := time.After(2 * time.Second)
	for i := 0; i < numWaiters; i++ {
		select {
		case err := <-done:
			if err != ErrPoolClosed {
				t.Errorf("waiter %d: expected ErrPoolClosed, got %v", i, err)
			}
		case <-timeout:
			t.Fatalf("only %d/%d waiters unblocked", i, numWaiters)
		}
	}

	t.Logf("All %d waiters properly unblocked", numWaiters)

	// Cleanup
	conn1.Close()
}

func TestContextCancelWithMultipleWaiters(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(0), WithMaxPool(2))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	// Exhaust the pool
	conn1, _ := pool.Get()
	conn2, _ := pool.Get()
	defer conn1.Close()
	defer conn2.Close()

	// Start 3 goroutines with different context timeouts
	type result struct {
		id      int
		err     error
		elapsed time.Duration
	}

	results := make(chan result, 3)

	// Waiter 1: 50ms timeout
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := pool.GetWithContext(ctx)
		results <- result{1, err, time.Since(start)}
	}()

	// Waiter 2: 100ms timeout
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := pool.GetWithContext(ctx)
		results <- result{2, err, time.Since(start)}
	}()

	// Waiter 3: 150ms timeout
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := pool.GetWithContext(ctx)
		results <- result{3, err, time.Since(start)}
	}()

	// Collect results
	for i := 0; i < 3; i++ {
		r := <-results
		if r.err != context.DeadlineExceeded {
			t.Errorf("waiter %d: expected DeadlineExceeded, got %v", r.id, r.err)
		}
		t.Logf("Waiter %d timed out after %v", r.id, r.elapsed)
	}
}

func TestDoubleCloseConnection(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(1), WithMaxPool(5))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Get()
	if err != nil {
		t.Fatalf("failed to get connection: %v", err)
	}

	// First close - should return to pool
	err = conn.Close()
	if err != nil {
		t.Errorf("first Close() failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	statsAfterFirstClose := pool.Stats()
	t.Logf("After first close: Active=%d, Idle=%d, InUse=%d",
		statsAfterFirstClose.Active, statsAfterFirstClose.Idle, statsAfterFirstClose.InUse)

	// Second close - should be a no-op or return error
	err = conn.Close()
	if err != nil {
		t.Logf("second Close() returned expected error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	statsAfterSecondClose := pool.Stats()
	t.Logf("After second close: Active=%d, Idle=%d, InUse=%d",
		statsAfterSecondClose.Active, statsAfterSecondClose.Idle, statsAfterSecondClose.InUse)

	// Stats should not change after second close
	if statsAfterFirstClose.Active != statsAfterSecondClose.Active {
		t.Errorf("stats changed after double close: %+v -> %+v",
			statsAfterFirstClose, statsAfterSecondClose)
	}

	// Using connection after double close should fail
	_, err = conn.Write([]byte("test"))
	if err == nil {
		t.Error("Write should fail on double-closed connection")
	}
}

func TestExtremeContention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping extreme contention test in short mode")
	}

	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(1), WithMaxPool(5))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	numGoroutines := 100
	iterationsPerGoroutine := 100

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*iterationsPerGoroutine)

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < iterationsPerGoroutine; j++ {
				conn, err := pool.Get()
				if err != nil {
					errors <- fmt.Errorf("goroutine %d: Get failed: %w", id, err)
					continue
				}

				// Simulate very brief use
				time.Sleep(1 * time.Millisecond)

				conn.Close()
			}
		}(i)
	}

	wg.Wait()
	elapsed := time.Since(start)
	close(errors)

	errorCount := 0
	for err := range errors {
		t.Logf("Error: %v", err)
		errorCount++
		if errorCount >= 10 {
			t.Log("... (more errors suppressed)")
			break
		}
	}

	if errorCount > 0 {
		t.Errorf("got %d errors during extreme contention", errorCount)
	}

	stats := pool.Stats()
	t.Logf("Extreme contention test completed in %v", elapsed)
	t.Logf("Final stats: Active=%d, Idle=%d, InUse=%d",
		stats.Active, stats.Idle, stats.InUse)
	t.Logf("Total operations: %d", numGoroutines*iterationsPerGoroutine)
	t.Logf("Operations/sec: %.0f", float64(numGoroutines*iterationsPerGoroutine)/elapsed.Seconds())

	// Pool should be stable
	if stats.Active > int(stats.MaxPool) {
		t.Errorf("pool exceeded MaxPool: Active=%d, MaxPool=%d", stats.Active, stats.MaxPool)
	}

	if stats.InUse != 0 {
		t.Errorf("expected all connections returned: InUse=%d", stats.InUse)
	}
}

func BenchmarkPoolConcurrent(b *testing.B) {
	listener, addr := createBenchmarkTestServer(b)
	defer listener.Close()

	pool, _ := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(10), WithMaxPool(300))
	defer pool.Close()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := pool.Get()
			if err != nil {
				b.Fatal(err)
			}
			conn.Write([]byte("test data"))
			conn.Close()
		}
	})
}

func createBenchmarkTestServer(t testing.TB) (net.Listener, string) {
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
