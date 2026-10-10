package singbridge

import (
	"context"

	"github.com/0xUnixIO/Xray-core/common"
	"github.com/0xUnixIO/Xray-core/common/buf"
	"github.com/0xUnixIO/Xray-core/common/net"
	"github.com/0xUnixIO/Xray-core/common/signal"
	"github.com/0xUnixIO/Xray-core/transport"
	B "github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
)

func CopyPacketConn(ctx context.Context, inboundConn net.Conn, link *transport.Link, destination net.Destination, serverConn net.PacketConn) error {
	conn, stop := WrapPacketLink(ctx, link, destination, inboundConn)
	defer stop()
	return ReturnError(bufio.CopyPacketConn(conn.ctx, conn, bufio.NewPacketConn(serverConn)))
}

// WrapPacketLink 给一条 UDP link 挂上 policy 的空闲计时。stop 在转发结束时调用。
func WrapPacketLink(ctx context.Context, link *transport.Link, destination net.Destination, conn net.Conn) (*PacketConnWrapper, func()) {
	ctx, cancel := context.WithCancel(ctx)
	w := &PacketConnWrapper{
		Reader: link.Reader,
		Writer: link.Writer,
		Dest:   destination,
		Conn:   conn,
		ctx:    ctx,
		timer:  signal.CancelAfterInactivity(ctx, cancel, timeoutsFrom(ctx).ConnectionIdle),
	}
	return w, func() {
		cancel()
		common.Interrupt(link.Reader)
		common.Interrupt(link.Writer)
	}
}

type PacketConnWrapper struct {
	buf.Reader
	buf.Writer
	net.Conn
	Dest   net.Destination
	cached buf.MultiBuffer
	ctx    context.Context
	timer  signal.ActivityUpdater
}

// ReadPacket 在 ctx 取消时中断阻塞读，避免对端不再写时 goroutine 泄漏。
// 取消由连接级空闲计时驱动，不再使用与另一方向流量无关的固定 60s。
func (w *PacketConnWrapper) ReadPacket(buffer *B.Buffer) (M.Socksaddr, error) {
	if w.cached != nil {
		mb, bb := buf.SplitFirst(w.cached)
		if bb == nil {
			w.cached = nil
		} else {
			buffer.Write(bb.Bytes())
			w.cached = mb
			var destination net.Destination
			if bb.UDP != nil {
				destination = *bb.UDP
			} else {
				destination = w.Dest
			}
			bb.Release()
			return ToSocksaddr(destination), nil
		}
	}

	// timeout
	type readResult struct {
		mb  buf.MultiBuffer
		err error
	}
	c := make(chan readResult, 1)
	go func() {
		mb, err := w.ReadMultiBuffer()
		c <- readResult{mb: mb, err: err}
	}()
	var mb buf.MultiBuffer
	var done <-chan struct{}
	if w.ctx != nil {
		done = w.ctx.Done()
	}
	select {
	case <-done:
		common.Close(w.Reader)
		common.Interrupt(w.Reader)
		return M.Socksaddr{}, w.ctx.Err()
	case result := <-c:
		if result.err != nil {
			return M.Socksaddr{}, result.err
		}
		mb = result.mb
	}
	if w.timer != nil {
		w.timer.Update()
	}

	nb, bb := buf.SplitFirst(mb)
	if bb == nil {
		return M.Socksaddr{}, nil
	} else {
		buffer.Write(bb.Bytes())
		w.cached = nb
		var destination net.Destination
		if bb.UDP != nil {
			destination = *bb.UDP
		} else {
			destination = w.Dest
		}
		bb.Release()
		return ToSocksaddr(destination), nil
	}
}

func (w *PacketConnWrapper) WritePacket(buffer *B.Buffer, destination M.Socksaddr) error {
	vBuf := buf.New()
	vBuf.Write(buffer.Bytes())
	endpoint := ToDestination(destination, net.Network_UDP)
	vBuf.UDP = &endpoint
	if err := w.Writer.WriteMultiBuffer(buf.MultiBuffer{vBuf}); err != nil {
		return err
	}
	if w.timer != nil {
		w.timer.Update()
	}
	return nil
}

func (w *PacketConnWrapper) Close() error {
	buf.ReleaseMulti(w.cached)
	return nil
}
