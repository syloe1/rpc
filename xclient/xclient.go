package xclient

import (
	"context"
	. "geerpc"
	"io"
	"reflect"
	"sync"
)

type XClient struct {
	d       Discovery          // 服务发现（从注册中心拿地址）
	mode    SelectMode         // 负载均衡模式（随机/轮询）
	opt     *Option            // 协议选项
	mu      sync.Mutex         // 保护下面的 map
	clients map[string]*Client // 连接复用池（key:地址，value:长连接）
}

var _ io.Closer = (*XClient)(nil)

func NewXClient(d Discovery, mode SelectMode, opt *Option) *XClient {
	return &XClient{
		d:       d,
		mode:    mode,
		opt:     opt,
		clients: make(map[string]*Client),
	}
}

func (x *XClient) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for key, client := range x.clients {
		_ = client.Close()
		delete(x.clients, key)
	}
	return nil
}
func (x *XClient) dial(rpcAddr string) (*Client, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	// 1. 看看连接池里有没有现成的连接
	client, ok := x.clients[rpcAddr]

	// 2. 如果有但不能用了，删掉
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(x.clients, rpcAddr)
		client = nil
	}

	// 3. 没有连接，新建一个，放进池子里
	if client == nil {
		client, _ = XDial(rpcAddr, x.opt)
		x.clients[rpcAddr] = client
	}

	return client, nil
}

// 跟一个固定的服务地址通信
func (x *XClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args, reply interface{}) error {
	//跟rpcAddr建立通信
	client, err := x.dial(rpcAddr)
	if err != nil {
		return err
	}
	//真正去调用远程方法
	return client.Call(ctx, serviceMethod, args, reply)
}

// 从注册中心拿一个可用的服务地址，在用call去通信
func (x *XClient) Get(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	rpcAddr, err := x.d.Get(x.mode)
	if rpcAddr == "" {
		return err
	}
	return x.call(rpcAddr, ctx, serviceMethod, args, reply)
}

// call = 跟固定地址通信（最底层）
// Get = 从服务发现拿地址，再通信（中间层）
// Call = 给用户用的入口（最上层）
// 选一个可用服务，发请求
func (x *XClient) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	return x.Get(ctx, serviceMethod, args, reply)
}

// 把请求发给所有在线的服务
func (x *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	//那带所有活着的服务地址列表
	servers, err := x.d.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var e error

	replyDone := reply == nil
	//创建可用取消的上下文
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			//创建一个返回值副本
			var cloneReply interface{}
			if reply != nil {
				cloneReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := x.call(rpcAddr, ctx, serviceMethod, args, cloneReply)
			//错误处理
			mu.Lock()
			if err != nil && e == nil {
				e = err
				//一出错，就取消所有服务的请求
				cancel()
			}
			//只要一个服务成功返回，把结果给reply标记完成，其他结果不再覆盖
			if err == nil && !replyDone {
				//把答案从 cloneReply 拿出来，放进 reply
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(cloneReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
	}
	wg.Wait()
	return e
}
