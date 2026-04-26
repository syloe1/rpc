package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

// Codec是一个对消息体进行编码的接口
// GobCodec handles gob-based RPC message encoding and decoding.
type GobCodec struct {
	conn io.ReadWriteCloser
	//防止阻塞，有缓冲的channel
	buf *bufio.Writer
	dec *gob.Decoder
	enc *gob.Encoder
}

// 实现Codec接口
// ReadHeader reads the header of the RPC message from the connection.
// ReadBody reads the body of the RPC message from the connection.
// Write writes the header and body of the RPC message to the connection.
var _ Codec = (*GobCodec)(nil)

// 参数 + 返回值一样 = 自动符合类型
func NewGobCodec(conn io.ReadWriteCloser) Codec {
	//写缓冲满了给conn去发送
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		//解码读数据必须使用原始连接，读不需要缓冲
		dec: gob.NewDecoder(conn),
		//编码写数据用缓冲Writer，提升性能
		enc: gob.NewEncoder(buf),
	}
}

//net -> conn -> dec -> 变量
//数据 -> enc -> buf -> Flush -> net
/*
ReadHeader(*Header) error
ReadBody(interface{}) error
Write(*Header, interface{}) error
*/
func (c *GobCodec) ReadHeader(h *Header) error {
	//c.dec gob解码器，从conn读字节转成 GO结构体
	return c.dec.Decode(h)
}

// Decode从网络读-> 填到你的变量里
func (c *GobCodec) ReadBody(body interface{}) error {
	return c.dec.Decode(body)
}

func (c *GobCodec) Write(h *Header, body interface{}) (err error) {
	defer func() {
		//刷新缓冲，把数据真正发送到网络
		if flushErr := c.buf.Flush(); flushErr != nil && err == nil {
			err = flushErr
		}
		//有错误关闭连接
		if err != nil {
			_ = c.Close()
		}
	}()
	//把Header序列化，写入缓冲buf
	if err = c.enc.Encode(h); err != nil {
		log.Println("rpc codec: gob error encoding header:", err)
		return err
	}
	//把body序列化，写入缓冲buf
	if err = c.enc.Encode(body); err != nil {
		log.Println("rpc codec: gob error encoding body:", err)
		return err
	}
	//触发defer，执行buf.Flush()给数据一次性发走
	return nil
}

func (c *GobCodec) Close() error {
	return c.conn.Close()
}

/*
write使用buf不是conn, 写缓冲批量发送，先放缓冲，
编码Header + Body放缓冲， 最后一次Flush发送，
减少网络系统调用，提升性能


为什么derfer里面flush，无论编解码是否error,最后都能刷新/清理
使用了命名返回值(err error)所以defer可以修改生效
*/
