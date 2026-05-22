package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

// MultiServersDiscovery = 服务发现器
// 功能：
// 1. 存一堆服务地址
// 2. 能随机选一个（RandomSelect）
// 3. 能轮询选一个（RoundRobinSelect）
// 4. 并发安全（加了读写锁）
type SelectMode int

const (
	RandomSelect SelectMode = iota
	RoundRobinSelect
)

// 服务发现接口
type Discovery interface {
	Refresh() error                      //手动刷新
	Update(servers []string) error       //更新服务地址
	Get(mode SelectMode) (string, error) //根据策略选一个服务
	GetAll() ([]string, error)
}

var _ Discovery = (*MultiServersDiscovery)(nil)

type MultiServersDiscovery struct {
	r       *rand.Rand   // 随机数生成器
	mu      sync.RWMutex // 读写锁（并发安全）
	servers []string     // 服务地址列表
	index   int          // 轮询用的下标
}

func (d *MultiServersDiscovery) Refresh() error {
	return nil
}
func (d *MultiServersDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	return nil
}

func (d *MultiServersDiscovery) Get(mode SelectMode) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no avaiable servers")
	}
	switch mode {
	case RandomSelect:
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect:
		s := d.servers[d.index%n]
		d.index = (d.index + 1) % n // 轮询 +1
		return s, nil
	default:
		return "", errors.New("rpc discovery: unknown select mode")
	}
}
func (d *MultiServersDiscovery) GetAll() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	servers := make([]string, len(d.servers), len(d.servers))
	//copy(dst, src)
	copy(servers, d.servers)
	return servers, nil
}

//	type MultiServersDiscovery struct {
//		r       *rand.Rand
//		mu      sync.RWMutex
//		servers []string // 服务地址列表
//		index   int
//	}
func NewMultiServerDiscovery(servers []string) *MultiServersDiscovery {
	d := &MultiServersDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	// 随机初始化轮询起点，避免所有客户端同时从0开始
	d.index = d.r.Intn(math.MaxInt32 - 1) // 随机初始化轮询起点
	return d
}
