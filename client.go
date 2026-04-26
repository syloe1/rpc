package geerpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"sync"
)

// 一次 RPC 调用的全部信息
type Call struct {
	Seq           uint64
	ServiceMethod string
	Args          interface{}
	Reply         interface{}
	Error         error
	Done          chan *Call
}

// 把自己扔进通道 → 告诉用户：调用完成！
func (call *Call) done() {
	call.Done <- call
}

type Client struct {
	cc       codec.Codec //编解码器
	opt      *Option     //协议选项
	sending  sync.Mutex  //发送锁（防止并发写乱包）
	header   *codec.Header
	mu       sync.Mutex //保护 pending 字典
	seq      uint64
	pending  map[uint64]*Call //正在进行中的调用（key: seq, value: Call）
	closing  bool
	shutdown bool
}

var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shutdown")

func (client *Client) Close() error {
	//防止多个goroutine同时调用Close
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.shutdown {
		return ErrShutdown
	}
	client.closing = true
	//关闭编解码器
	return client.cc.Close()
}

func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

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
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	//把之前按等级的RPC调用从等待列表中删除
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// 异常断开，终止所有正在等待的RPC调用，全部报错返回
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	//锁住pending
	client.mu.Lock()
	defer client.mu.Unlock()

	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

// 给服务端发请求
func (client *Client) send(call *Call) {
	client.sending.Lock()
	defer client.sending.Unlock()
	//获取唯一序号seq
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}
	//组装请求头
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	//发送到网络
	if err := client.cc.Write(client.header, call.Args); err != nil {
		call := client.removeCall(seq)
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
		if err = client.cc.ReadHeader(&h); err != nil {
			return
		}
		call := client.removeCall(h.Seq)
		switch {
		case call == nil:
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body  " + err.Error())
			}
			call.done()
		}

	}
	//连接断开或者出错
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
	//发送请求
	client.send(call)
	return call
}

// 发起RPC调用，死等结果回来-> 直接返回错误
func (client *Client) Call(serviceMethod string, args, reply interface{}) error {
	call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done
	return call.Error
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
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	//拿到对应编解码器的构造函数
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error: ", err)
		return nil, err
	}
	//发送配置给服务端， RPC握手
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

// 创建客户端实例 + 初始化所有成员 + 启动后台监听协程
func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		header:  &codec.Header{},
		pending: make(map[uint64]*Call),
	}
	go client.receive()
	return client
}

// 连接 RPC 服务器 → 处理配置 → 建立好可用的客户端
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	//建立 TCP 网络连接
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	//全资源清理
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	return NewClient(conn, opt)
}
