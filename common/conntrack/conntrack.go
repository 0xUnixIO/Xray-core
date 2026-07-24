// Package conntrack 按用户追踪活跃的入站连接，并支持按用户强制断开。
//
// 存在的理由：proxy.UserManager 的 RemoveUser 只把用户从 inbound validator
// 摘掉，而鉴权只在连接建立时做一次——已建立的连接不再回查 validator，会一直
// 跑到自己结束。对流量超限这类需要「立刻停止该用户流量」的场景，仅靠 RemoveUser
// 无法生效，而重启整个实例又会波及同节点上的其他用户。
//
// 用法（三个接入点）：
//   - 入站 worker 建立连接时 Register，连接结束时 Unregister；
//   - dispatcher 在鉴权完成后 BindUser，把连接归属到具体用户；
//   - 控制面调用 Kick 断开某用户的全部存量连接。
package conntrack

import (
	"context"
	"net"
	"sync"
)

// entry 描述一条被追踪的活跃连接。
type entry struct {
	email  string
	cancel context.CancelFunc
	conn   net.Conn
}

// tracker 是全局连接注册表。key 为 session ID，同一进程内唯一。
type tracker struct {
	mu      sync.Mutex
	entries map[uint32]*entry
}

var global = &tracker{entries: make(map[uint32]*entry)}

// Register 登记一条新建立的入站连接。email 此时通常尚未确定，
// 由后续的 BindUser 补齐。cancel 与 conn 允许为 nil。
func Register(sid uint32, cancel context.CancelFunc, conn net.Conn) {
	if sid == 0 {
		return
	}
	global.mu.Lock()
	global.entries[sid] = &entry{cancel: cancel, conn: conn}
	global.mu.Unlock()
}

// BindUser 在鉴权完成后把连接归属到具体用户。email 为空则忽略。
// 重复绑定同一 sid 是安全的（覆盖写）。
func BindUser(sid uint32, email string) {
	if sid == 0 || email == "" {
		return
	}
	global.mu.Lock()
	if e, ok := global.entries[sid]; ok {
		e.email = email
	}
	global.mu.Unlock()
}

// Unregister 注销一条已结束的连接。
func Unregister(sid uint32) {
	if sid == 0 {
		return
	}
	global.mu.Lock()
	delete(global.entries, sid)
	global.mu.Unlock()
}

// Kick 断开指定用户的所有存量连接，返回被断开的连接数。
//
// 同时取消连接 ctx 并关闭底层 conn：前者让阻塞在 ctx 上的转发协程退出，
// 后者确保正卡在 socket 读写上的协程立即收到错误。
func Kick(email string) int {
	if email == "" {
		return 0
	}
	global.mu.Lock()
	var victims []*entry
	for sid, e := range global.entries {
		if e.email == email {
			victims = append(victims, e)
			delete(global.entries, sid)
		}
	}
	global.mu.Unlock()

	for _, e := range victims {
		if e.cancel != nil {
			e.cancel()
		}
		if e.conn != nil {
			_ = e.conn.Close()
		}
	}
	return len(victims)
}

// CountByUser 返回指定用户当前的活跃连接数，用于观测与测试。
func CountByUser(email string) int {
	if email == "" {
		return 0
	}
	global.mu.Lock()
	defer global.mu.Unlock()
	n := 0
	for _, e := range global.entries {
		if e.email == email {
			n++
		}
	}
	return n
}
