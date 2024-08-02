package geerpc

import (
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"sync"
)

// 一次 rpc 调用所需要的信息
type Call struct {
	Seq           uint64
	ServiceMethod string
	Args          interface{}
	Reply         interface{}
	Error         error
	Done          chan *Call
}

// 支持异步调用
func (call *Call) done() {
	call.Done <- call
}

type Client struct {
	cc       codec.Codec      // 消息的编解码器
	opt      *Option          // 编码信息
	sending  sync.Mutex       // 互斥锁，防止多个请求报文混淆
	header   codec.Header     // 消息头
	mu       sync.Mutex       // Client 锁
	seq      uint64           // 请求数量
	pending  map[uint64]*Call // 存储未处理的请求
	closing  bool             // 用户主动关闭
	shutdown bool             // 错误导致关闭
}

var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shut down")

// 关闭连接
func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true
	return client.cc.Close()
}

// 检查连接是否可用
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

// 注册 Call
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// 删除 Call
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// 服务端或客户端发生错误时调用
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

// 客户端接受响应
func (client *Client) reveive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeaer(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq)
		switch {
		case call == nil:
			// call 不存在，可能是没有发送完整
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			// call 存在，但服务端出错
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			// call 存在，且处理正常
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading boey " + err.Error())
			}
			call.done()
		}
	}
	client.terminateCalls(err)
}
