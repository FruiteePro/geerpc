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

const MagicNumber = 0x3bef5c

// 消息的编解码方式
type Option struct {
	MagicNumber int
	CodecType   codec.Type
}

var DefaultOption = &Option{
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
}

// Server 端实现
type Server struct{}

// 返回一个新的 Server
func NewServer() *Server {
	return &Server{}
}

var DefaultServer = NewServer()

// 接受 Listener 对象， for 循环等待 socket 连接建立
// 处理过程交给 ServeConn 函数
func (server *Server) Accept(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}
		go server.ServeConn(conn)
	}
}

// 对外方便调用的 accept 函数
func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }()
	var opt Option
	// 反序列化得到 Option 实例
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error", err)
		return
	}
	// 检查 MagicNumber 是否正确
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magin number %x", opt.MagicNumber)
		return
	}
	// 检查 CodeType 是否正确
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	// 处理连接
	server.serveCodec(f(conn))
}

// invalidRequest 是在发生错误时用于响应参数的占位符。
var invalidRequest = struct{}{}

// 对连接进行处理
func (server *Server) serveCodec(cc codec.Codec) {
	// 回复请求锁，保证只同时进行一个回复
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	// 一次会有多个请求
	for {
		// 读取一个请求
		req, err := server.readRequest(cc)
		if err != nil {
			// 尽力回复，header 解析失败时才 break
			if req == nil {
				break
			}
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		// 协程并发 处理
		go server.handleRequest(cc, req, sending, wg)
	}
	wg.Wait()
	_ = cc.Close()
}

// request 结构体
type request struct {
	h            *codec.Header // request 头
	argv, replyv reflect.Value // argv 和 replyv
}

// 读取请求头
func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeaer(&h); err != nil {
		log.Println("rpc server: read header error", err)
		return nil, err
	}
	return &h, nil
}

// 读取请求
func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	// 得到请求头
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	// 声明 request 结构体
	req := &request{h: h}
	// TODO: now we don't know the type of request argv
	// day 1, just suppose it's string
	req.argv = reflect.New(reflect.TypeOf(""))
	// 读取 body
	if err = cc.ReadBody(req.argv.Interface()); err != nil {
		log.Println("rpc server: read avgv err", err)
	}
	return req, nil
}

// 回复请求
func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}

// 处理请求
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	// TODO, should call registered rpc methods to get the right replyv
	// day 1, just print argv and send a hello message
	defer wg.Done()
	log.Println(req.h, req.argv.Elem())
	req.replyv = reflect.ValueOf(fmt.Sprintf("geerpc resp %d", req.h.Seq))
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
}
