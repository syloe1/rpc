package geerpc

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func assertClient(condition bool, msg string, v ...interface{}) {
	if !condition {
		panic(fmt.Sprintf("assertion failed: "+msg, v...))
	}
}

func TestClient_dialTimeout(t *testing.T) {
	t.Parallel()

	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	f := func(conn net.Conn, opt *Option) (client *Client, err error) {
		_ = conn.Close()
		time.Sleep(2 * time.Second)
		return nil, nil
	}

	t.Run("timeout", func(t *testing.T) {
		_, err := dialTimeout(f, "tcp", l.Addr().String(), &Option{ConnectTimeout: time.Second})
		assertClient(err != nil && strings.Contains(err.Error(), "connect timeout"), "expect a timeout error")
	})

	t.Run("zero means no limit", func(t *testing.T) {
		_, err := dialTimeout(f, "tcp", l.Addr().String(), &Option{ConnectTimeout: 0})
		assertClient(err == nil, "expect no timeout error")
	})
}

type Bar int

func (b Bar) Timeout(argv int, reply *int) error {
	time.Sleep(2 * time.Second)
	return nil
}

func startServer(addr chan string) {
	var b Bar
	_ = Register(&b)
	l, _ := net.Listen("tcp", ":0")
	addr <- l.Addr().String()
	Accept(l)
}

func TestClient_Call(t *testing.T) {
	t.Parallel()

	addrCh := make(chan string)
	go startServer(addrCh)
	addr := <-addrCh
	time.Sleep(time.Second)

	t.Run("client timeout", func(t *testing.T) {
		client, err := Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		var reply int
		err = client.Call(ctx, "Bar.Timeout", 1, &reply)
		assertClient(err != nil && strings.Contains(err.Error(), ctx.Err().Error()), "expect a timeout error")
	})

	t.Run("server handle timeout", func(t *testing.T) {
		client, err := Dial("tcp", addr, &Option{
			HandleTimeout: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()

		var reply int
		err = client.Call(context.Background(), "Bar.Timeout", 1, &reply)
		assertClient(err != nil && strings.Contains(err.Error(), "handle timeout"), "expect a timeout error")
	})
}
