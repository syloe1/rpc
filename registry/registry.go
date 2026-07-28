package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// 注册中心本地
type GeeRegistry struct {
	timeout time.Duration
	mu      sync.Mutex
	servers map[string]*ServerItem
}

// 服务实例 用map存实例信息， 用时间戳判断存活
type ServerItem struct {
	Addr  string
	start time.Time
}

const (
	defaultPath    = "/tuturpc/rpc"
	defaultTimeout = time.Minute * 5
)

func New(timeout time.Duration) *GeeRegistry {
	return &GeeRegistry{
		timeout: timeout,
		servers: make(map[string]*ServerItem),
	}
}

var DefaultGeeRegister = New(defaultTimeout)

// 注册/续约服务 putServer
func (r *GeeRegistry) putServer(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[addr]
	if s == nil {
		r.servers[addr] = &ServerItem{
			Addr:  addr,
			start: time.Now(),
		}
	} else {
		s.start = time.Now() // 更新时间戳，视为续约
	}
}

// 获取存活的服务列表
func (r *GeeRegistry) aliveServers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var alive []string
	for addr, s := range r.servers {
		//超时时间为0 永不过期
		// s.start：服务启动时间
		// Add(r.timeout)：加上允许存活的时间
		// After(time.Now())：是否在当前时间之后
		if r.timeout == 0 || s.start.Add(r.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			delete(r.servers, addr)
		}
	}
	sort.Strings(alive)
	return alive
}

// HTTP接口
func (r *GeeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		//strings.Join(数组, 分隔符)
		w.Header().Set("X-Geerpc-Servers", strings.Join(r.aliveServers(), ","))
	case "POST":
		//从请求头里面拿到服务地址
		addr := req.Header.Get("X-Geerpc-Server")
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		//注册服务
		r.putServer(addr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// 把注册中心绑定到指定的HTTP路径上
func (r *GeeRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, r)
	log.Println("rpc registry path: ", registryPath)
}
func HandleHTTP() {
	DefaultGeeRegister.HandleHTTP(defaultPath)
}

// 心跳机制 向服务端主动注册中心报告自己存活的关键部分
// 定时给注册中心发消息， 防止被删除
func Heartbeat(registry, addr string, duration time.Duration) {
	if duration == 0 { //设置心跳间隔
		duration = defaultTimeout - time.Duration(1)*time.Minute
	}
	var err error
	err = sendHeartbeat(registry, addr)
	go func() {
		//创建一个定时器，每个duration时间响应一次
		t := time.NewTicker(duration)

		for err == nil {
			<-t.C //等待定时器响，定时器不响就不走
			err = sendHeartbeat(registry, addr)
		}
	}()
}

// 打印日志：我（addr）正在给注册中心发心跳
// 创建一个 HTTP POST 请求
// 把我的地址放到请求头里
// 发给注册中心
// 告诉注册中心：我还活着！
func sendHeartbeat(registry, addr string) error {
	log.Println(addr, "send heart beat to registry", registry)
	httpClient := &http.Client{}
	//创建一个POST请求
	req, _ := http.NewRequest("POST", registry, nil)
	//把地址放在请求头里面
	req.Header.Set("X-Geerpc-Server", addr)
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server: heart beat err:", err)
		return err
	}
	return nil
}
