package geerpc

import (
	"encoding/json"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"reflect"
	"sync"
)

const MagicNumber = 0x3bef5c //魔法数，校验协议合法性

// 约定的通信阅读， 一个是暗号，一个是选择什么序列化方式
type Option struct {
	MagicNumber int
	CodecType   codec.Type //序列化方式eg Gob
}

// 默认配置， GOb序列化
var DefaultOption = &Option{
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
}

type Server struct{} //服务器

//创建一个Server实例，支持多实例隔离
/*
s1 := geerpc.NewServer()
s2 := geerpc.NewServer()
*/
func NewServer() *Server {
	return &Server{}
}

var DefaultServer = NewServer()

// 处理连接·
// 接收连接 —> 握手校验 -> 初始化编解码器 -> 正式处理RPC
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() {
		_ = conn.Close() //go运行嵌套接口,conn网络连接
	}()

	var opt Option
	//读取并校验握手信息
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	//校验魔法数，是不是合法请求
	if opt.MagicNumber != MagicNumber {
		log.Println("rpc server: invalid magic number %x: ", opt.MagicNumber)
		return
	}
	//根据序列化类型创建编解码器
	//func NewGobCodec(conn io.ReadWriteCloser) Codec
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type  %s", opt.CodecType)
		return
	}
	//交给serveCodec处理RPC请求
	//server.serveCodec(处理Codec实例)
	server.serveCodec(f(conn))
	/*
		没有defer CLose()连接一直占着，服务器崩溃
		魔法数的作用是防止乱链接，垃圾请求，错误端口访问
	*/
}

// 错误响应占位符
var invalidRequest = struct{}{}

func (server *Server) serveCodec(cc codec.Codec) {
	/*
		不用sending锁，多个goroutine同时往连接写数据，数据错乱
		wg关闭连接前，必须等所有正在处理的请求都响应完成
	*/
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	for {
		//读取请求
		req, err := server.readRequest(cc)
		if err != nil {
			//client断开
			if req == nil {
				break
			}
			//请求头错误，返回错误响应
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		//defer wg.Done()在handleRequest里面
		//异步处理每个goroutine，一个连接可以处理多个请求
		go server.handleRequest(cc, req, sending, wg)
	}
	wg.Wait()
	_ = cc.Close() //关闭编解码器
}

type request struct {
	h            *codec.Header
	argv, replyv reflect.Value //请求参数，响应参数
	//一个rpc请求把所有需要的东西打包在一起，argv是客户端传过来的参数
	//replyv是服务端要返回的结果,为什么用reflect.Value因为编译时不知道参数是什么类型
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		//io.EOF / io.ErrUnexpectedEOF = 客户端正常断开
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Printf("rpc server: read header error: ", err)
		}
		return nil, err
	}

	return &h, nil
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	//创建Request对象，里面有两个空位置argv, replyv
	req := &request{h: h}
	/*
		reflect.New根据一个类型创建一个指向改类型的指针
		reflect.TypeOF("")拿到string
		创建一个string指针的reflect.Value类型
		Interface()把反射值还原为原生类型
	*/
	req.argv = reflect.New(reflect.TypeOf(""))
	//读请求体 return c.dec.Decode(body)
	if err = cc.ReadBody(req.argv.Interface()); err != nil {
		log.Println("rpc server: read argv err: ", err)
	}
	return req, nil
}

func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	//互斥锁防止并发写乱包，一个TCP连接可能同时有多个goroutine要响应
	sending.Lock()
	defer sending.Unlock()
	//序列化写入响应
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error: ", err)
	}
}

func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()
	//reflect.Elem()解开指针，得到指向的值
	//req.argv = reflect.New拿到指针，用Elem拿到你要的数据
	log.Println(req.h, req.argv.Elem())
	//构造响应内容
	req.replyv = reflect.ValueOf(fmt.Sprintf("geerpc resp %d", req.h.Seq))
	//发送响应
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
}

// 来一个连接，就开启一个协程处理
// 底层服务
func (server *Server) Accept(lis net.Listener) {
	for {
		//监听端口，接受客户端连接
		//lis.Accept阻塞等待，没人连接就卡在这里，有人连接返回一个conn对象
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error: ", err)
			continue
		}
		//每个连接启一个goroutine处理
		go server.ServeConn(conn)
	}

}

// 给用户调用的接口，启动服务
func Accept(lis net.Listener) {
	DefaultServer.Accept(lis)
}

/*
1
server := geerpc.NewServer()
server.Accept(lis)
2
geerpc.Accept(lis)
*/

//req.argv.Interface()：把反射值对象（reflect.Value）转换为普通的 Go 接口类型
