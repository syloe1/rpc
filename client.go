package geerpc

// RPC客户端， 负责 连接服务器， 发送请求， 接受响应， 管理所有RPC调用
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 一次 RPC 调用的全部信息
type Call struct {
	Seq           uint64 // 唯一序号
	ServiceMethod string // 调用哪个方法
	Args          interface{}
	Reply         interface{}
	Error         error
	Done          chan *Call // 调用完成通知
}

// 把自己扔进通道 → 告诉用户：调用完成！
func (call *Call) done() {
	select {
	case call.Done <- call:
	default: //没人接收， 直接跳过
	}
}

// 整个 RPC 调用流程：
// 建立连接（Dial）
// 发送请求（Call/Go）
// 等待响应（receive）
// 返回结果
// RPC客户端本体
type Client struct {
	cc       codec.Codec //编解码器
	opt      *Option     //协议选项
	sending  sync.Mutex  //发送锁（防止并发写乱包）
	header   *codec.Header
	mu       sync.Mutex       //保护 pending 字典
	seq      uint64           //请求自增ID
	pending  map[uint64]*Call //正在进行中的调用（key: seq, value: Call）
	closing  bool
	shutdown bool
}

var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shutdown")

type clientResult struct {
	client *Client
	err    error
}
type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

// 带超时连接函数
// 建立TCP连接 + 初始化客户端， 必须在规定时间内完成， 否则直接超时失败
func dialTimeout(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	//带超时建立TCP连接
	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	//创建通道来接收创建结果
	ch := make(chan clientResult)
	go func() {
		client, err := f(conn, opt)
		ch <- clientResult{client: client, err: err}
	}()
	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	select {
	//超时
	case <-time.After(opt.ConnectTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeout)
	case result := <-ch:
		return result.client, result.err
	}
}

// 关闭客户端
func (client *Client) Close() error {
	//防止多个goroutine同时调用Close
	client.mu.Lock()
	defer client.mu.Unlock()
	//已经关闭直接返回
	if client.shutdown {
		return ErrShutdown
	}
	//标记关闭中
	client.closing = true
	//关闭编解码器
	return client.cc.Close()
}

// closing = 正在关闭中
// shutdown = 已经彻底关闭、完全死掉
// 判断客户端是否可用
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

// 给RPC请求分配序号 -> 存入等待列表 -> 等待服务端返回
// 注册调用，存入pending map, 返回序号
func (client *Client) registerCall(call *Call) (uint64, error) {
	//防止并发goroutine
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	//分配seq和RPC请求
	call.Seq = client.seq
	//把Call存进pending map
	//pending  map[uint64]*Call
	//正在进行中的调用（key: seq, value: Call）
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// 根据序号移除调用， 返回调用对象
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	//把之前按等级的RPC调用从等待列表中删除
	call := client.pending[seq]

	//delete(map, key)
	delete(client.pending, seq)
	return call
}

// 异常断开，终止所有正在等待的RPC调用，全部报错返回
func (client *Client) terminateCalls(err error) {
	//锁住发送操作
	client.sending.Lock()
	defer client.sending.Unlock()
	//锁住pending
	client.mu.Lock()
	defer client.mu.Unlock()

	client.shutdown = true
	//type Call struct {
	//	Seq           uint64 // 唯一序号
	//	ServiceMethod string // 调用哪个方法
	//	Args          interface{}
	//	Reply         interface{}
	//	Error         error
	//	Done          chan *Call // 调用完成通知
	//}
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

// 给服务端发请求
func (client *Client) send(call *Call) {
	//发送锁， 同一时间只能有一个goroutine执行发送
	client.sending.Lock()
	defer client.sending.Unlock()
	//获取唯一序号seq
	//RPC请求注册到pending队列
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}
	//组装请求头
	// RPC请求头
	//type Header struct {
	//	ServiceMethod string //Service.Method
	//	Seq           uint64 //序列号
	//	Error         string //
	//}
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	//发送到网络
	if err := client.cc.Write(client.header, call.Args); err != nil {
		call := client.removeCall(seq) //并发安全
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		//头里面有服务名， seq,错误信息
		if err = client.cc.ReadHeader(&h); err != nil {
			return
		}
		call := client.removeCall(h.Seq)
		switch {
		//没找到call
		case call == nil:
			//丢弃body
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			call.Error = errors.New(h.Error)
			//丢弃body
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			//把响应读到call.Reply里
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body  " + err.Error())
			}
			call.done()
		}

	}
	//连接断开或者出错
	//终止所有正在等待的RPC调用
	client.terminateCalls(err)
}

// 创建一个异步RPC调用，交给send发送，立即返回
func (client *Client) Go(serviceMethod string, args interface{}, reply interface{}, done chan *Call) *Call {
	if done == nil {
		//未传递通道给个有缓冲的
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		//无缓冲通道直接panic
		log.Panic("rpc client: done channel is unbuffered")
	}
	//把RPC调用需要的信息打包
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	//同步：调用函数 → 等着结果回来 → 才能继续执行
	//异步：调用函数 → 立刻返回 → 你自己去等结果（不卡住）
	//发送请求
	//为什么不适用go client.send() send里面有锁， 确定同一时间只能有一个goroutine发送数据
	client.send(call)
	//直接返回， 不等结果
	return call
}

// RPC发送必须串行， 基于TCP流协议， 不能并发发送， 会粘包， 乱序，
// 发起RPC调用，死等结果回来-> 直接返回错误
// 同步调用 异步调用Go
// // 同步调用：发起RPC，死等结果 / 超时 / 取消
func (client *Client) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		//	// 上下文超时/取消了
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	case call := <-call.Done:
		return call.Error
	}
}
func parseOptions(opts ...*Option) (*Option, error) {
	//没传配置 / 传了 nil → 返回默认配置
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	//传了多个配置 → 报错
	if len(opts) != 1 {
		return nil, errors.New("only one option is unallowed")
	}
	//拿到用户传递的配置
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// 返回可用的client
// 创建客户端
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	//拿到对应编解码器的构造函数
	//NewCodecFuncMap map[Type]NewCodecFunc
	//type NewCodecFunc func(io.ReadWriteCloser) Codec
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		//编解码接口为找到
		log.Println("rpc client: codec error: ", err)
		return nil, err
	}
	//发送配置给服务端， RPC握手
	//json.NewEncoder(conn)创建一个 “JSON 打包器”，输出口直接接到网络管子上
	//.Encode(opt)把opt->JSOn
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	//参数是Codec, opt
	return newClientCodec(f(conn), opt), nil
}

// 创建客户端实例 + 初始化所有成员 + 启动后台监听协程
func newClientCodec(cc codec.Codec, opt *Option) *Client {
	//type Client struct {
	//	cc       codec.Codec //编解码器
	//	opt      *Option     //协议选项
	//	sending  sync.Mutex  //发送锁（防止并发写乱包）
	//	header   *codec.Header
	//	mu       sync.Mutex       //保护 pending 字典
	//	seq      uint64           //请求自增ID
	//	pending  map[uint64]*Call //正在进行中的调用（key: seq, value: Call）
	//	closing  bool
	//	shutdown bool
	//}
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		header:  &codec.Header{},
		pending: make(map[uint64]*Call),
	}
	go client.receive() //启动后台监听
	return client
}

// 连接 RPC 服务器 → 处理配置 → 建立好可用的客户端
// 普通TCP连接
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	//func dialTimeout(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error)
	return dialTimeout(NewClient, network, address, opts...)
}

// HTTP隧道RPC客户端
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	//发送HTTP COnnection
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	if err == nil {
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	return nil, err
}

// 函数	连接方式	底层调用	用途
// Dial	普通 TCP 直连	NewClient	内网、直连服务
// DialHTTP	HTTP 隧道连接	NewHTTPClient	穿透代理、防火墙、外网环境
// HTTP隧道连接

// 为什么要设计这个函数？
// 因为：
// 很多公司 / 防火墙只开放 80/443 HTTP 端口
// 普通 TCP 连接被拦住，连不上
// HTTP 隧道可以伪装成 HTTP 流量，顺利穿透
// DialHTTP 就是为了解决这个问题！
func DialHTTP(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

// 字符串连接 http@127.0.0.1:8080
// 协议@地址连接
func XDial(rpcAddr string, opts ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format: %s, expect protocol@addr", rpcAddr)
	}
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		return DialHTTP("tcp", addr, opts...)
	default:
		return Dial(protocol, addr, opts...)
	}
}
