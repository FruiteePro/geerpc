package codec

import "io"

// 请求头
type Header struct {
	ServiceMethod string // 方法名
	Seq           uint64 // 请求序号
	Error         string // 错误信息
}

// 编解码接口
type Codec interface {
	io.Closer
	ReadHeaer(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

// Coder 构造函数
// 客户端和服务端可以通过 Codec 的 Type 得到构造函数，从而创建 Codec 实例。
type NewCodecFunc func(io.ReadWriteCloser) Codec

type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json"
)

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
