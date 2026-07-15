// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rpc

/*
	Some HTML presented at http://machine:port/debug/rpc
	Lists services, their methods, and some statistics, still rudimentary.
*/

import (
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strings"
)

const debugText = `<html>
	<body>
	<title>Services</title>
	{{range .}}
	<hr>
	Service {{.Name}}
	<hr>
		<table>
		<th align=center>Method</th><th align=center>Calls</th>
		{{range .Method}}
			<tr>
			<td align=left font=fixed>{{.Name}}({{.Type.ArgType}}, {{.Type.ReplyType}}) error</td>
			<td align=center>{{.Type.NumCalls}}</td>
			</tr>
		{{end}}
		</table>
	{{end}}
	</body>
	</html>`

var debug = template.Must(template.New("RPC debug").Parse(debugText))

// If set, print log statements for internal and I/O errors.
var debugLog = false

//单个RPC方法封装
/*
type methodType struct {
	sync.Mutex // protects counters
	method     reflect.Method
	ArgType    reflect.Type
	ReplyType  reflect.Type
	numCalls   uint
}
*/
type debugMethod struct {
	Type *methodType // 底层RPC内核的方法元数据（存ArgType/ReplyType/NumCalls调用计数）
	Name string      // 方法名，如 "GetUser"
}

// 一组方法
type methodArray []debugMethod

/*

type service struct {
	name   string                 // name of service
	rcvr   reflect.Value          // receiver of methods for the service
	typ    reflect.Type           // type of the receiver
	method map[string]*methodType // registered methods
}
*/
//单个RPC服务封装
type debugService struct {
	Service *service      // 底层内核服务对象
	Name    string        // 服务名，如 UserService
	Method  []debugMethod // 该服务下全部方法列表
}

// 全部服务集合
type serviceArray []debugService

func (s serviceArray) Len() int           { return len(s) }
func (s serviceArray) Less(i, j int) bool { return s[i].Name < s[j].Name }
func (s serviceArray) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func (m methodArray) Len() int           { return len(m) }
func (m methodArray) Less(i, j int) bool { return m[i].Name < m[j].Name }
func (m methodArray) Swap(i, j int)      { m[i], m[j] = m[j], m[i] }

type debugHTTP struct {
	*Server
}

/*
 Server represents an RPC Server.
type Server struct {
	serviceMap sync.Map   // map[string]*service
	reqLock    sync.Mutex // protects freeReq
	freeReq    *Request
	respLock   sync.Mutex // protects freeResp
	freeResp   *Response
}
*/
// Runs at /debug/rpc
func (server debugHTTP) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Build a sorted version of the data.
	var services serviceArray
	server.serviceMap.Range(func(snamei, svci any) bool {
		svc := svci.(*service)
		ds := debugService{svc, snamei.(string), make([]debugMethod, 0, len(svc.method))}
		for mname, method := range svc.method {
			ds.Method = append(ds.Method, debugMethod{method, mname})
		}
		//字典序排序
		slices.SortFunc(ds.Method, func(a, b debugMethod) int {
			return strings.Compare(a.Name, b.Name)
		})
		services = append(services, ds)
		return true
	})
	slices.SortFunc(services, func(a, b debugService) int {
		return strings.Compare(a.Name, b.Name)
	})
	err := debug.Execute(w, services)
	if err != nil {
		fmt.Fprintln(w, "rpc: error executing template:", err.Error())
	}
}
