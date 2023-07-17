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

## Usage	

Here's a simple example of how to use Netpool in your Go application:
```go
package main

import (
	"log"

	"github.com/yudhasubki/netpool"
)

func main() {
	netpool, err := netpool.New(func() (net.Conn, error) {
		return net.Dial("tcp", "localhost:80")
	}, 
		netpool.WithMaxPool(10), // default 15
		netpool.WithMinPool(5),  // default 5
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
	_, err = conn.Read(buffer)
	if err != nil {
		log.Fatal(err)
	}
}
```

## Contributing
Contributions are welcome! If you find a bug or have a suggestion for improvement, please open an issue or submit a pull request. Make sure to follow the existing coding style and write tests for any new functionality.

## License
This project is licensed under the MIT License. See the LICENSE file for details.

## Acknowledgments
NetPool is inspired by various connection pool implementations in the Golang ecosystem. We would like to thank the authors of those projects for their contributions and ideas.