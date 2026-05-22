package xclient

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// 从注册中心拉取服务列表的服务发现器
type GeeRegistryDiscovery struct {
	*MultiServersDiscovery               // 继承：负载均衡（随机/轮询）
	registry               string        // 注册中心地址
	timeout                time.Duration // 刷新超时时间
	lastUpdate             time.Time     // 最后一次更新时间
}

const defaultUpdateTimeout = time.Second * 10

func NewGeeRegistryDiscovery(registerAddr string, timeout time.Duration) *GeeRegistryDiscovery {
	if timeout == 0 {
		timeout = defaultUpdateTimeout
	}
	d := &GeeRegistryDiscovery{
		MultiServersDiscovery: NewMultiServerDiscovery(make([]string, 0)),
		registry:              registerAddr,
		timeout:               timeout,
	}
	return d
}

func (d *GeeRegistryDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers //同步d.servers, 同步时间
	d.lastUpdate = time.Now()
	return nil
}
func (d *GeeRegistryDiscovery) Refresh() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	//未过期不刷新
	if d.lastUpdate.Add(d.timeout).After(time.Now()) {
		return nil
	}
	log.Println("rpc registry refresh from registryL:", d.registry)
	//send http get请求，去注册中心拿到最新的服务列表
	resp, err := http.Get(d.registry)
	if err != nil {
		log.Println("rpc registry refresh err:", err)
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	//从响应头取出服务列表
	//strings.Split(字符串, 分隔符)
	//把一个长字符串按照指定符号切开， 变成一个字符串数组
	servers := strings.Split(resp.Header.Get("X-Geerpc-Servers"), ",")
	//清空旧列表， 把新的服务地址存进去(去除空值和空格)
	d.servers = make([]string, 0, len(d.servers))
	for _, server := range servers {
		// 去掉这个地址前后的空格、换行、制表符，
		//    如果剩下的不是空字符串，就保留
		if strings.TrimSpace(server) != "" {
			d.servers = append(d.servers, strings.TrimSpace(server))
		}
	}
	d.lastUpdate = time.Now()
	return nil
}

func (d *GeeRegistryDiscovery) Get(mode SelectMode) (string, error) {
	//拿服务前，先刷新
	if err := d.Refresh(); err != nil {
		return "", err
	}
	return d.MultiServersDiscovery.Get(mode)
}
func (d *GeeRegistryDiscovery) GetAll() ([]string, error) {
	if err := d.Refresh(); err != nil {
		return nil, err
	}
	return d.MultiServersDiscovery.GetAll()
}
