package netpool

import (
	"errors"
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
	if stats.TotalCreated != 3 {
		t.Errorf("expected 3 total connections, got %d", stats.TotalCreated)
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

	pool.Put(conn, nil)

	if pool.Len() != 2 {
		t.Errorf("expected 2 idle connections after Put, got %d", pool.Len())
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

	conn2, err := pool.Get()
	if err != nil {
		t.Fatalf("expected new connection, got error: %v", err)
	}

	stats := pool.Stats()
	if stats.TotalCreated != 2 {
		t.Errorf("expected 2 total connections, got %d", stats.TotalCreated)
	}
	if stats.InUse != 2 {
		t.Errorf("expected 2 in-use connections, got %d", stats.InUse)
	}

	pool.Put(conn1, nil)
	pool.Put(conn2, nil)
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

	pool.Put(conn, errors.New("connection broken"))

	finalStats := pool.Stats()

	if finalStats.TotalCreated != initialStats.TotalCreated-1 {
		t.Errorf("expected TotalCreated to decrease")
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

	pool.Put(conn1, nil)

	select {
	case <-blocked:
		if conn3 == nil {
			t.Fatal("expected connection after unblock")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Get() didn't unblock")
	}

	pool.Put(conn2, nil)
	pool.Put(conn3, nil)
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

	errors := make(chan error, goroutines*iterations)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				conn, err := pool.Get()
				if err != nil {
					errors <- err
					continue
				}

				// Use the connection
				msg := []byte("test")
				_, err = conn.Write(msg)
				if err != nil {
					pool.Put(conn, err)
					errors <- err
					continue
				}

				buf := make([]byte, 1024)
				conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				_, err = conn.Read(buf)

				pool.Put(conn, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	errorCount := 0
	for err := range errors {
		t.Logf("error during test: %v", err)
		errorCount++
	}

	if errorCount > 0 {
		t.Errorf("got %d errors during concurrent operations", errorCount)
	}

	stats := pool.Stats()
	t.Logf("Final stats - Total: %d, Idle: %d, InUse: %d",
		stats.TotalCreated, stats.Idle, stats.InUse)
}

func TestClose_Connections(t *testing.T) {
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
	hook := func(conn net.Conn) error {
		hookCalled++
		// Test the connection
		_, err := conn.Write([]byte("hook test"))
		return err
	}

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(2), WithDialHooks(hook))

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

	pool.Put(conn1, nil)
	pool.Put(conn2, nil)
	pool.Put(conn3, nil)
}

func TestStatsAccuracy(t *testing.T) {
	listener, addr := createTestServer(t)
	defer listener.Close()

	pool, err := New(func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}, WithMinPool(2), WithMaxPool(5))

	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	// Initial state
	stats := pool.Stats()
	if stats.TotalCreated != 2 || stats.Idle != 2 || stats.InUse != 0 {
		t.Errorf("initial stats incorrect: %+v", stats)
	}

	conn1, _ := pool.Get()
	conn2, _ := pool.Get()

	stats = pool.Stats()
	if stats.TotalCreated != 2 || stats.Idle != 0 || stats.InUse != 2 {
		t.Errorf("stats after Get incorrect: %+v", stats)
	}

	pool.Put(conn1, nil)

	stats = pool.Stats()
	if stats.TotalCreated != 2 || stats.Idle != 1 || stats.InUse != 1 {
		t.Errorf("stats after Put incorrect: %+v", stats)
	}

	conn3, _ := pool.Get()
	conn4, _ := pool.Get()

	stats = pool.Stats()
	if stats.TotalCreated != 3 || stats.Idle != 0 || stats.InUse != 3 {
		t.Errorf("stats after creating new incorrect: %+v", stats)
	}

	pool.Put(conn2, nil)
	pool.Put(conn3, nil)
	pool.Put(conn4, nil)
}
