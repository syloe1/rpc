package main

import (
	"context"
	"fmt"
	"geerpc"
	"geerpc/registry"
	"geerpc/xclient"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// 服务
type Foo int

type Args struct{ Num1, Num2 int }

// Sum和Sleep提供的两个远程方法
// RPC方法格式func (t *T) Method(arg T1, reply *T2) error
// RPC方法固定格式
func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func (f Foo) Sleep(args Args, reply *int) error {
	time.Sleep(time.Second * time.Duration(args.Num1))
	*reply = args.Num1 + args.Num2
	return nil
}

// 开始RPC服务
// wg在main等待服务端启动， 注册中心-> 服务端->客户端
func startServer(registryAddr string, wg *sync.WaitGroup) {
	var foo Foo
	//开一个TCP端口
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatal("network error:", err)
	}
	//创建RPC-Server
	server := geerpc.NewServer()
	//注册服务
	if err := server.Register(&foo); err != nil {
		log.Fatal("register error:", err)
	}
	//注册服务到注册中心
	//去注册中心报到
	registry.Heartbeat(registryAddr, "tcp@"+l.Addr().String(), 0)
	wg.Done() //通知main， main可以跑
	//接受RPC请求
	server.Accept(l) //死循环等待客户端
}

// xc *xclient.XClient,  // RPC 客户端（工具人）
// ctx context.Context,  // 上下文（控制超时、取消）
// typ string,           // 类型：call 或 broadcast
// serviceMethod string, // 要调用的方法：如 Foo.Sum
// args *Args            // 参数：Num1, Num2
func fooCall(xc *xclient.XClient, ctx context.Context, typ, serviceMethod string, args *Args) {
	var reply int
	var err error
	switch typ {
	case "call":
		err = xc.Call(ctx, serviceMethod, args, &reply)
	case "broadcast":
		err = xc.Broadcast(ctx, serviceMethod, args, &reply)
	}
	if err != nil {
		log.Printf("%s %s error: %v", typ, serviceMethod, err)
	} else {
		log.Printf("%s %s success: %d + %d = %d", typ, serviceMethod, args.Num1, args.Num2, reply)
	}
}

// 这个函数干一件事：
// 启动 5 个并发请求，通过注册中心找到服务端，
// 随机选一个服务端调用 Foo.Sum 求和
// 创建服务发现
func call(registryAddr string) {
	//服务发现
	d := xclient.NewGeeRegistryDiscovery(registryAddr, 0)
	//支持负载均衡的RPC客户端
	xc := xclient.NewXClient(d, xclient.RandomSelect, nil)
	defer func() { _ = xc.Close() }()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fooCall(xc, context.Background(), "call", "Foo.Sum", &Args{Num1: i, Num2: i * i})
		}(i)
	}
	//等所有调用完成后再退出
	wg.Wait()
}

func broadcast(registryAddr string) {
	d := xclient.NewGeeRegistryDiscovery(registryAddr, 0)
	xc := xclient.NewXClient(d, xclient.RandomSelect, nil)
	defer func() { _ = xc.Close() }()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fooCall(xc, context.Background(), "broadcast", "Foo.Sum", &Args{Num1: i, Num2: i * i})
			//**我只给你 2 秒时间执行任务！
			//到点没完成，直接打断、报错、不等了！**
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			fooCall(xc, ctx, "broadcast", "Foo.Sleep", &Args{Num1: i, Num2: i * i})
		}(i)
	}
	wg.Wait()
}

// 注册中心
func startRegistry(wg *sync.WaitGroup) {
	//监听9999端口
	l, err := net.Listen("tcp", ":9999")
	if err != nil {
		log.Fatal("registry listen error:", err)
	}
	//注册HTTP Handler
	registry.HandleHTTP()
	wg.Done()
	//启动HTTP服务
	_ = http.Serve(l, nil)
}

func main() {
	log.SetFlags(0)
	registryAddr := "http://localhost:9999/tuturpc/rpc"

	var wg sync.WaitGroup
	wg.Add(1)
	go startRegistry(&wg)
	wg.Wait() //等待注册中心启动

	time.Sleep(time.Second)
	wg.Add(2)
	go startServer(registryAddr, &wg)
	go startServer(registryAddr, &wg)
	wg.Wait()

	time.Sleep(time.Second)
	fmt.Println("=====================call=====================================")
	//负载均衡调用
	call(registryAddr)
	fmt.Println("=====================call done========== ===========================")
	//所有节点一起调用
	fmt.Println("=====================broadcast=====================================")
	broadcast(registryAddr)
	fmt.Println("=====================broadcast done=====================================")
}

// Client
//   ↓
// Registry 获取服务列表
//   ↓
// 随机选一个 Server
//   ↓
// RPC调用
//   ↓
// 返回结果

// Client
//   ↓
// 拿到全部服务
//   ↓
// 并发调用所有服务
//   ↓
// 任意失败/超时
//   ↓
// 返回错误
