package geerpc

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

// 保证 Client 实现了 Close 方法
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

// client 注销 call
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

// 创建 Client 实例
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invilid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}
	// 发送 options 到 server
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Panicln("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

// 创建 client 编解码器
func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	go client.reveive()
	return client
}

// 处理 option 参数
func parseOptions(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// Dial 连接到指定网络地址的 RPC 服务器
// func Dial(network, address string, opts ...*Option) (client *Client, err error) {
// 	opt, err := parseOptions(opts...)
// 	if err != nil {
// 		return nil, err
// 	}
// 	conn, err := net.Dial(network, address)
// 	if err != nil {
// 		return nil, err
// 	}
// 	// 如果 client 为 nil 关闭连接
// 	defer func() {
// 		if client == nil {
// 			_ = conn.Close()
// 		}
// 	}()
// 	return NewClient(conn, opt)
// }

// client 发送 call
func (client *Client) send(call *Call) {
	client.sending.Lock()
	defer client.sending.Unlock()

	// 注册 call
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// 初始化请求头
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	// 编码并发送请求
	if err := client.cc.Write(&client.header, call.Args); err != err {
		call := client.removeCall(seq)
		// call 也许会返回 nil，通常意味着写入部分失败、
		// client 已收到响应并处理了
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// Go 异步调用函数
// 返回代表调用的 Call 结构。
func (client *Client) Go(serverMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serverMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

// 调用指定函数，等待函数完成，并返回其错误状态。
func (client *Client) Call(ctx context.Context, serverMethod string, args, reply interface{}) error {
	call := client.Go(serverMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	case call := <-call.Done:
		return call.Error
	}
}

type clientResult struct {
	client *Client
	err    error
}

type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

func dialTimeout(f newClientFunc, newtwork, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	// 创建连接
	conn, err := net.DialTimeout(newtwork, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	// 如果 client 为空，连接关闭
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	// 协程通信管道
	ch := make(chan clientResult)
	// 协程创建 client
	go func() {
		client, err := f(conn, opt)
		ch <- clientResult{client: client, err: err}
	}()
	// 没有超时限制
	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	// 有超时限制
	select {
	case <-time.After(opt.ConnectTimeout):
		// 如果 time.After 信道先得到消息，说明超时
		return nil, fmt.Errorf("rpc server: connect timeout: expect within %s", opt.ConnectTimeout)
	case result := <-ch:
		// 没有超时
		return result.client, result.err
	}
}

// Dial 连接到指定网络地址的 RPC 服务器
func Dial(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewClient, network, address, opts...)
}

func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
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

func DialHTTP(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

func XDial(rpcAddr string, opt ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s', expect protocol@addr", rpcAddr)
	}
	protocal, addr := parts[0], parts[1]
	switch protocal {
	case "http":
		return DialHTTP("tcp", addr, opt...)
	default:
		return Dial(protocal, addr, opt...)
	}
}
