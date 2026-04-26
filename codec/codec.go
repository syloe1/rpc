package codec

import "io"

type Header struct {
	ServiceMethod string //Service.Method
	Seq           uint64 //序列号
	Error         string //
}

//对消息体进行编码的接口Codec， 实现不同的codec实例
type Codec interface {
	/*
		type Closer interface {
			Close() error
		}
	*/
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

//传入一个连接，返回一个Codec，。 Codec构造函数
//NewCodecFunc是函数类型
//函数参数是网络连接conn, 返回是Codec编解码接口
type NewCodecFunc func(io.ReadWriteCloser) Codec

//gob / json
type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json"
)

//全局map,根据类型找到对应的构造函数

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec //注册gob编解码器
}
