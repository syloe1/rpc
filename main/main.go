package main

import (
	"fmt"
	"geerpc"
	"log"
	"net"
	"sync"
	"time"
)

// 启动rpc服务端
func startServer(addr chan string) {
	// pick a free port
	// Listen(network, address string) (Listener, error)
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatal("network error:", err)
	}
	//l.Addr () = 拿到服务器的地址（IP + 端口）
	//	log.Println("start rpc server on", l.Addr())
	//l.Addr().String() = 把它变成字符串
	addr <- l.Addr().String()
	//func Accept(lis net.Listener) = 启动服务
	geerpc.Accept(l)
}

func main() {
	//清空所有标志
	log.SetFlags(0)
	addr := make(chan string)
	// 启动服务端（协程）
	go startServer(addr)

	// 连接 TCP 服务器
	//conn, _ := net.Dial("tcp", <-addr)
	client, err := geerpc.Dial("tcp", <-addr)
	if err != nil {
		log.Fatal("dial error:", err)
	}
	defer func() { _ = client.Close() }()

	// 等 1 秒，确保服务端完全启动
	time.Sleep(time.Second)
	// send options
	//json.NewEncoder得到*Encoder , .Encode转Json + 发送数据
	//	_ = json.NewEncoder(conn).Encode(geerpc.DefaultOption)
	//返回一个对消息体进行编码的接口Codec
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			//构造请求参数
			args := fmt.Sprintf("geerpc req %d", i)
			var reply string
			//远程调用RPC方法Foo.Sum
			if err := client.Call("Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error ", err)
			}
			log.Println("reply:", reply)
		}(i)
	}
	wg.Wait()
}
