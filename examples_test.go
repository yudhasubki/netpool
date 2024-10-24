package netpool_test

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/yudhasubki/netpool"
)

func init() {
	go func() {
		l, err := net.Listen("tcp", ":2000")
		if err != nil {
			log.Fatal(err)
		}
		defer l.Close()
		// Wait for a connection.
		conn, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		buf := make([]byte, 4096)
		conn.Read(buf)
		conn.Write([]byte("hello world"))
		conn.Close()
	}()
}

func ExampleNew() {
	netpool, err := netpool.New(func() (net.Conn, error) {
		return net.Dial("tcp", "localhost:2000")
	},
		netpool.WithMaxPool(2),          // default 15
		netpool.WithMinPool(1),          // default 5
		netpool.WithChecks(time.Second), // default none
	)
	if err != nil {
		panic(err)
	}
	defer netpool.Close()

	conn, err := netpool.Get()
	defer netpool.Put(conn, err)
	if err != nil {
		log.Fatal(err)
	}

	_, err = conn.Write([]byte("dial"))
	if err != nil {
		log.Fatal(err)
	}

	// Read the response from the connection
	buffer := make([]byte, 4096)
	var n int
	n, err = conn.Read(buffer)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Read %s\n", buffer[:n])

	netpool.Put(conn, err)
	// Output:
	// Read hello world
}
