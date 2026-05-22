package geerpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

// RPC元信息
type methodType struct {
	method    reflect.Method // 方法本身（反射对象）
	ArgType   reflect.Type   // 参数类型（如 Args）
	ReplyType reflect.Type   // 返回值类型（如 int）
	numCalls  uint64         // 调用次数（统计用）
}

// 统计调用次数
func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}

// 动态创建参数
func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// arg may be a pointer type, or a value type
	//指针类型
	if m.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(m.ArgType.Elem())
	} else {
		//值类型
		//new generate ptr, Elem() get the value of ptr
		argv = reflect.New(m.ArgType).Elem()
	}
	return argv
}

// 生成一个返回值盒子 RPC规则强制要求： 返回值必须是指针
func (m *methodType) newReplyv() reflect.Value {
	//RPC强制返回值必须是指针
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

// RPC服务的包装
type service struct {
	name   string                 // 服务名（例如 "Foo"）
	typ    reflect.Type           // 结构体类型（Foo）
	rcvr   reflect.Value          // 结构体实例（&Foo{}）
	method map[string]*methodType // 这个服务的所有方法（Sum、Sleep...）
}

// 创建RPC服务
func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)                  //拿到结构体实例            //拿到实例
	s.name = reflect.Indirect(s.rcvr).Type().Name() //拿到结构体名字
	s.typ = reflect.TypeOf(rcvr)                    //拿到类型

	//检查服务名必须大写(导出)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	//注册成RPC方法
	s.registerMethods()
	return s
}

// 合法的RPC方法
// func (s *Service) Method(arg ArgType, reply *ReplyType) error
// 筛选并注册符合规则的 RPC 方法
func (s *service) registerMethods() {
	s.method = make(map[string]*methodType) // 创建方法字典

	// 遍历结构体的所有方法
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i) // 拿到第 i 个方法
		mType := method.Type      // 拿到方法的签名（参数、返回值）

		// ====================== 规则判断开始 ======================

		// 规则1：方法必须是 3 个参数，1 个返回值
		// (自身, arg, reply) + 返回 error
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue // 不符合 → 跳过
		}

		// 规则2：返回值必须是 error 类型
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue // 不符合 → 跳过
		}

		// 拿到第2个参数（用户参数）、第3个参数（返回值）
		argType, replyType := mType.In(1), mType.In(2)

		// 规则3：参数 & 返回值 必须是导出类型（大写）或内置类型
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue // 不符合 → 跳过
		}

		// ====================== 规则判断结束 ======================

		// 符合所有规则 → 注册成 RPC 方法
		//type methodType struct {
		//	method    reflect.Method // 方法本身（反射对象）
		//	ArgType   reflect.Type   // 参数类型（如 Args）
		//	ReplyType reflect.Type   // 返回值类型（如 int）
		//	numCalls  uint64         // 调用次数（统计用）
		//}

		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

// 反射调用方法
func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1) //调用次数 + 1
	f := m.method.Func               //拿到要执行的函数
	//rcvr结构体实例 	 argv参数 	 replyv返回值盒子
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv}) //反射调用
	//returnValues[0]  // 就是函数返回的 error
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}

// 大写开头 / 内置类型
func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}
