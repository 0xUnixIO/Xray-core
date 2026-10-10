package singbridge

import (
	"context"
	"net"

	"github.com/0xUnixIO/Xray-core/common"
	"github.com/0xUnixIO/Xray-core/common/buf"
	"github.com/0xUnixIO/Xray-core/common/session"
	"github.com/0xUnixIO/Xray-core/common/signal"
	"github.com/0xUnixIO/Xray-core/common/task"
	"github.com/0xUnixIO/Xray-core/core"
	"github.com/0xUnixIO/Xray-core/features/policy"
	"github.com/0xUnixIO/Xray-core/transport"
)

type timeoutOverrideKey struct{}

func contextWithTimeouts(ctx context.Context, timeouts policy.Timeout) context.Context {
	return context.WithValue(ctx, timeoutOverrideKey{}, timeouts)
}

func timeoutsFrom(ctx context.Context) policy.Timeout {
	if timeouts, ok := ctx.Value(timeoutOverrideKey{}).(policy.Timeout); ok {
		return timeouts
	}
	if inst := core.FromContext(ctx); inst != nil {
		if pm, ok := inst.GetFeature(policy.ManagerType()).(policy.Manager); ok && pm != nil {
			level := uint32(0)
			if ib := session.InboundFromContext(ctx); ib != nil && ib.User != nil {
				level = ib.User.Level
			}
			return pm.ForLevel(level).Timeouts
		}
	}
	return policy.SessionDefault().Timeouts
}

// CopyConn 在入站 link 和落地连接之间双向转发。
// 空闲时间取自 policy.connIdle，任一方向有字节都会刷新；一边结束后改用对应的单向宽限。
// 不再使用按次 Read 的固定 300s 截止，那个截止与另一方向的流量无关。
func CopyConn(ctx context.Context, _ net.Conn, link *transport.Link, serverConn net.Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	timeouts := timeoutsFrom(ctx)
	timer := signal.CancelAfterInactivity(ctx, cancel, timeouts.ConnectionIdle)

	requestDone := func() error {
		defer timer.SetTimeout(timeouts.DownlinkOnly)
		err := buf.Copy(link.Reader, buf.NewWriter(serverConn), buf.UpdateActivity(timer))
		closeWrite(serverConn)
		return err
	}
	responseDone := func() error {
		defer timer.SetTimeout(timeouts.UplinkOnly)
		return buf.Copy(buf.NewReader(serverConn), link.Writer, buf.UpdateActivity(timer))
	}

	err := task.Run(ctx, requestDone, task.OnSuccess(responseDone, task.Close(link.Writer)))
	common.Interrupt(link.Reader)
	common.Interrupt(link.Writer)
	_ = serverConn.Close()
	return ReturnError(err)
}

func closeWrite(conn net.Conn) {
	if conn == nil {
		return
	}
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := conn.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}
