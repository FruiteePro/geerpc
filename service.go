package geerpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

type methodType struct {
	method    reflect.Method
	ArgType   reflect.Type
	ReplyType reflect.Type
	numCalls  uint64
}

func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}

// 创建传入参数实例
func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// arg 可能是指针，也可能是值
	if m.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(m.ArgType.Elem())
	} else {
		argv = reflect.New(m.ArgType).Elem()
	}
	return argv
}

// 创建返回参数实例
func (m *methodType) newReplyv() reflect.Value {
	// replyv 必须是一个指针
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

type service struct {
	name   string                 // 结构体名字
	typ    reflect.Type           // 结构体类型
	rcvr   reflect.Value          // 结构体本身的实例名
	method map[string]*methodType // 存储映射的结构体所有符合条件的方法
}

// 构造函数
func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	s.typ = reflect.TypeOf(rcvr)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	s.registerMethods()
	return s
}

// 注册方法
func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mType := method.Type
		// 检查方法输入参数和输出参数数量
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		// 检查方法返回值类型是否为 error
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		// 检查参数类型
		argType, replyType := mType.In(1), mType.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

// 检查类型是否是导出的类型或者内建类型
func isExportedOrBuiltinType(t reflect.Type) bool {
	// ast.IsExported 检查标识符是否是导出的
	// t.PkgPath 检查是否是内建类型（如 int、string 等），如果是，则返回空字符串 “”
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

// 通过反射调用方法
func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValue := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	if errInter := returnValue[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
