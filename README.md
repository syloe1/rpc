# geeRPC

一个从零实现的轻量级 RPC 学习项目，目标是把下面这些能力串起来：

- 基于 TCP 的 RPC 调用
- 自定义请求头与 `gob` 编解码
- 反射注册服务与方法调用
- 客户端超时控制
- 服务端处理超时控制
- HTTP 方式承载 RPC
- 注册中心与心跳
- 带服务发现、负载均衡、广播能力的 `xclient`

当前仓库代码已经可以直接运行，并且 `main/main.go` 演示的是：

- 启动一个 HTTP 注册中心
- 启动两个 RPC 服务实例
- 服务实例向注册中心定时发送心跳
- 客户端通过注册中心发现服务
- 进行普通调用 `Call`
- 进行广播调用 `Broadcast`

## 项目结构

```text
.
├── client.go                # RPC 客户端，支持超时、HTTP CONNECT、XDial
├── server.go                # RPC 服务端，支持 TCP/HTTP、请求处理、超时控制
├── service.go               # 反射注册服务与方法调用
├── debug.go                 # /debug/geerpc 调试页
├── client_test.go           # 客户端超时测试
├── service_test.go          # service 反射调用测试
├── codec/
│   ├── codec.go             # 编解码接口与消息头定义
│   └── gob.go               # gob 编解码实现
├── registry/
│   └── registry.go          # 注册中心与心跳
├── xclient/
│   ├── discovery.go         # 多实例发现与随机/轮询选择
│   ├── discovery_gee.go     # 基于注册中心的服务发现
│   └── xclient.go           # 支持 Call / Broadcast 的高级客户端
└── main/
    └── main.go              # 当前项目演示入口
```

## 核心概念

### 1. 一次 RPC 调用包含什么

每次请求都会带两部分：

- `Header`
  - `ServiceMethod`：例如 `Foo.Sum`
  - `Seq`：请求序号，用来把请求和响应对应起来
  - `Error`：服务端返回的错误信息
- `Body`
  - 真实参数或返回值

定义位置：

- [codec/codec.go](/C:/Users/WK112/Desktop/test/codec/codec.go)

### 2. 服务是怎么注册的

服务端通过反射注册结构体方法，满足下面条件的方法才能成为 RPC 方法：

- 方法所属类型必须可导出
- 方法本身必须可导出
- 入参固定两个
- 第二个参数必须是指针
- 返回值固定一个 `error`

例如：

```go
type Foo int

func (f Foo) Sum(args Args, reply *int) error {
    *reply = args.Num1 + args.Num2
    return nil
}
```

相关代码：

- [service.go](/C:/Users/WK112/Desktop/test/service.go)

### 3. 客户端怎么把响应对应回原请求

客户端内部维护一个 `pending map[seq]*Call`：

- 发送请求前，先为请求分配一个 `seq`
- 把请求放进 `pending`
- 收到响应后，根据响应头里的 `seq` 找回原请求
- 把结果写入 `reply`，再通知调用方完成

相关代码：

- [client.go](/C:/Users/WK112/Desktop/test/client.go)

## 当前实现的能力

### 基础 TCP RPC

服务端：

- `Accept(lis net.Listener)`

客户端：

- `Dial(network, address, opts...)`

适合原始 TCP 连接，不走 HTTP。

### HTTP 承载 RPC

服务端：

- `HandleHTTP()`
- 内部使用 `CONNECT /_geerpc_`

客户端：

- `DialHTTP(network, address, opts...)`
- `XDial("http@host:port")`

注意：

- “HTTP 注册中心” 和 “HTTP 承载 RPC” 不是一回事
- 当前 [main/main.go](/C:/Users/WK112/Desktop/test/main/main.go) 演示的是：
  - 注册中心通过 HTTP 工作
  - RPC 服务本身仍然是 TCP 地址 `tcp@...`

### 超时控制

支持两类超时：

- 客户端连接超时：`Option.ConnectTimeout`
- 客户端调用超时：`context.WithTimeout(...)`
- 服务端处理超时：`Option.HandleTimeout`

相关代码：

- [client.go](/C:/Users/WK112/Desktop/test/client.go)
- [server.go](/C:/Users/WK112/Desktop/test/server.go)

### 注册中心

注册中心提供两个动作：

- `GET /_geerpc_/registry`
  - 返回当前存活服务列表
- `POST /_geerpc_/registry`
  - 服务实例发送心跳

相关代码：

- [registry/registry.go](/C:/Users/WK112/Desktop/test/registry/registry.go)

### `xclient`

`xclient` 是更高层的客户端封装，支持：

- 服务发现
- 随机选择实例
- 轮询选择实例
- 广播到所有实例

相关代码：

- [xclient/discovery.go](/C:/Users/WK112/Desktop/test/xclient/discovery.go)
- [xclient/discovery_gee.go](/C:/Users/WK112/Desktop/test/xclient/discovery_gee.go)
- [xclient/xclient.go](/C:/Users/WK112/Desktop/test/xclient/xclient.go)

## 运行示例

项目入口：

- [main/main.go](/C:/Users/WK112/Desktop/test/main/main.go)

在仓库根目录执行：

```powershell
go run main/main.go
```

这段示例会做这些事：

1. 启动注册中心，监听 `:9999`
2. 启动两个 RPC 服务实例
3. 两个服务实例向注册中心发送心跳
4. 客户端通过注册中心拿到可用服务列表
5. 执行普通调用 `Foo.Sum`
6. 执行广播调用 `Foo.Sum`
7. 执行广播调用 `Foo.Sleep`，并通过 `context` 观察超时效果

你大概率会看到类似输出：

```text
rpc registry path:  /_geerpc_/registry
rpc server: register Foo.Sleep
rpc server: register Foo.Sum
tcp@[::]:xxxx send heart beat to registry http://localhost:9999/_geerpc_/registry
rpc registry refresh from registryL: http://localhost:9999/_geerpc_/registry
call Foo.Sum success: 1 + 1 = 2
broadcast Foo.Sum success: 4 + 16 = 20
broadcast Foo.Sleep error: rpc client: call failed: context deadline exceeded
```

## 测试

运行全部测试：

```powershell
go test ./...
```

只看客户端超时相关测试：

```powershell
go test -v -run TestClient ./...
```

当前测试主要覆盖：

- 连接超时 `ConnectTimeout`
- 客户端调用超时 `context.WithTimeout`
- 服务端处理超时 `HandleTimeout`
- `service` 反射注册与方法调用

## 调试方式

### 1. 看调用链

理解项目最有效的方法，是只追一条成功请求：

```text
Client.Call
-> Client.Go
-> Client.send
-> codec.Write
-> Server.Accept
-> Server.ServeConn
-> server.readRequest
-> server.findService
-> service.call
-> Foo.Sum
-> server.sendResponse
-> client.receive
```

### 2. 打日志

推荐临时在这些函数里加日志：

- `Client.send`
- `Client.receive`
- `Server.ServeConn`
- `server.readRequest`
- `server.handleRequest`
- `service.call`

重点打印：

- `ServiceMethod`
- `Seq`
- 参数值
- 返回值
- 超时错误

### 3. 看调试页

当服务端通过 `HandleHTTP()` 提供 HTTP 接口时，还会暴露：

- `/debug/geerpc`

相关代码：

- [debug.go](/C:/Users/WK112/Desktop/test/debug.go)

这个页面可以看到：

- 当前注册了哪些服务
- 每个服务有哪些方法
- 每个方法被调用了多少次

## 注册中心调用流程

当前 `main/main.go` 的完整流程可以记成：

```text
startRegistry
-> registry.HandleHTTP

startServer
-> server.Register(&foo)
-> registry.Heartbeat(registryAddr, "tcp@addr", ...)
-> server.Accept

call / broadcast
-> NewGeeRegistryDiscovery(registryAddr, ...)
-> discovery.Refresh()
-> GET registry
-> NewXClient(...)
-> XClient.Call / XClient.Broadcast
-> XDial("tcp@addr")
-> Client.Call(...)
```

## 后续可以继续扩展的方向

- 支持更多编解码方式，例如 `json`
- 支持注册中心的主动下线
- 支持负载均衡策略扩展
- 为 `xclient` 和 `registry` 补更多测试
- 整理项目中的乱码注释
- 增加更明确的 example，例如单独演示：
  - 纯 TCP RPC
  - HTTP-RPC
  - 注册中心 + `xclient`

## 说明

这个项目很适合拿来学习 RPC 的核心实现细节，因为它把网络、编解码、反射、超时、注册中心、服务发现这些概念都串在了一起。  
如果你正在一边跟教程一边写，最推荐的学习方式不是一口气读完全部代码，而是每次只追一条调用链，再通过日志和测试把它“跑亮”。
