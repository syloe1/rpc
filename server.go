package geerpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"

	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

// RPC协议魔数
const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber    int           // 魔数校验
	CodecType      codec.Type    // 编解码方式（gob/json等）
	ConnectTimeout time.Duration //0 means no limit
	HandleTimeout  time.Duration //处理超时
}

// 10s超时 + gob编解码
var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType, // 默认使用 gob 编解码
	ConnectTimeout: time.Second * 10,
}

// Server represents an RPC Server.
type Server struct {
	serviceMap sync.Map //存储注册RPC服务 key = 服务名， value = 服务对象
}

// NewServer returns a new Server.
func NewServer() *Server {
	return &Server{}
}

// DefaultServer is the default instance of *Server.
var DefaultServer = NewServer()

// 处理单个连接
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }()
	var opt Option
	//读取客户端的option配置
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	//校验魔数
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return
	}
	//创建对应的解码器
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	server.serveCodec(f(conn), &opt)
}

// invalidRequest is a placeholder for response argv when error occurs
var invalidRequest = struct{}{}

// 解析请求-->找到服务-->调用方法
// 一个连接可以发送多个RPC请求
func (server *Server) serveCodec(cc codec.Codec, opt *Option) {
	sending := new(sync.Mutex) // 发送锁， 保证同时只有一个goroutine发送， 不然TCP粘包， 乱序
	//
	wg := new(sync.WaitGroup) // wait until all request are handled
	//一个TCP连接可以发N个RPC请求
	for {
		//读取一个完整请求
		req, err := server.readRequest(cc) // 读客户端发来的请求
		if err != nil {
			if req == nil {
				break // it's not possible to recover, so close the connection
			}
			req.h.Error = err.Error()
			// 回复错误信息
			server.sendResponse(cc, req.h, invalidRequest, sending) // 开协程！异步处理！
			continue
		}
		//启动协程
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg, opt.HandleTimeout)
	}
	wg.Wait()
	_ = cc.Close()
}

// request stores all information of a call
type request struct {
	h            *codec.Header // header of request
	argv, replyv reflect.Value // argv and replyv of request
	mtype        *methodType
	svc          *service
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

// 客户端说：“我要调用 Foo.Sum”
// 这个函数就帮服务端找到：
// 服务：Foo 结构体
// 方法：Sum 函数
// 查找服务
func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	//拆分UserService.GetUser -> 服务名 = UserService,方法名=GetUser
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
		return
	}
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	//从serviceMap中查找服务
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}
	svc = svci.(*service)
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}

// 解析请求
func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	//读取请求头
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	//. 创建 request 对象
	req := &request{h: h}
	//根据serviceMethod找到对应的service和method
	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
	//type request struct {
	//	h            *codec.Header // header of request
	//	argv, replyv reflect.Value // argv and replyv of request
	//	mtype        *methodType
	//	svc          *service
	//}

	if err != nil {
		return req, err
	}
	//根据methodType生成参数和返回值盒子
	//type methodType struct {
	//	method    reflect.Method // 方法本身（反射对象）
	//	ArgType   reflect.Type   // 参数类型（如 Args）
	//	ReplyType reflect.Type   // 返回值类型（如 int）
	//	numCalls  uint64         // 调用次数（统计用）
	//}
	//

	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReplyv()

	// make sure that argvi is a pointer, ReadBody need a pointer as parameter
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		//Addr()取地址，变成指针
		argvi = req.argv.Addr().Interface()
	}
	//ReadBody必须传递地址
	//二进制 -> 解码 -> 写入argvi
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read body err:", err)
		return req, err
	}
	return req, nil
}

// 服务端把【响应头 + 结果 / 错误】发送回客户端
func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}

// RPC请求调用service.call成功就返回结果，失败就返回错误
// 服务端方法调用超时，防止卡死
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	//调用service.call -> 执行业务方法
	called := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		// ↓↓↓ 真正调用你的业务代码：Foo.Sum / Foo.Sleep ↓↓↓
		err := req.svc.call(req.mtype, req.argv, req.replyv) //调用本地方法
		called <- struct{}{}
		if err != nil {
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			sent <- struct{}{}
			return
		}
		//成功， 回传返回值
		server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
		sent <- struct{}{}
	}()
	if timeout == 0 {
		<-called
		<-sent
		return
	}
	select {
	case <-time.After(timeout): //超时了
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
	case <-called: //方法正常执行完
		<-sent
	}
}

// 死循环监听客户端连接
// 来一个连接，就开一个协程去处理
// 永远不退出，直到服务器关闭
func (server *Server) Accept(lis net.Listener) {
	for {
		//阻塞等待客户端连接
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}
		//来一个连接,启动一个goroutine处理
		go server.ServeConn(conn)
	}
}

// Accept accepts connections on the listener and serves requests
// for each incoming connection.
func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

// 把普通结构体变成RPC服务
func (server *Server) Register(rcvr interface{}) error {
	//把你的结构体 -> RPC服务
	s := newService(rcvr)
	//存进map，不能重复注册
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

// Register publishes the receiver's methods in the DefaultServer.
func Register(rcvr interface{}) error { return DefaultServer.Register(rcvr) }

const (
	connected        = "200 Connected to Gee Rpc"
	defaultRPCPath   = "/tuturpc/rpc"
	defaultDebugPath = "/debug/geerpc"
)

// HTTP隧道支持， 通过HTTP协议传输RPC
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	//HTTP连接抢过来， 变成裸TCP用来跑RPC
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking ", req.RemoteAddr, ":", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	server.ServeConn(conn)
}

func (server *Server) HandleHTTP() {
	// 1. 注册 RPC 隧道入口：客户端用 HTTP 连接 RPC
	http.Handle(defaultRPCPath, server)
	// 2. 注册调试页面：供查看服务状态、调用统计
	http.Handle(defaultDebugPath, debugHTTP{server})
	log.Println("rpc server debug path:", defaultDebugPath)
}

func HandleHTTP() {
	DefaultServer.HandleHTTP()
}
