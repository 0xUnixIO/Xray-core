package anytls

import (
	"context"
	"net"
	"testing"
	"time"

	anytlspadding "github.com/anytls/sing-anytls/padding"
	anytlssession "github.com/anytls/sing-anytls/session"
	satomic "github.com/sagernet/sing/common/atomic"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/0xUnixIO/Xray-core/common"
	"github.com/0xUnixIO/Xray-core/common/buf"
	xnet "github.com/0xUnixIO/Xray-core/common/net"
	"github.com/0xUnixIO/Xray-core/features/policy"
	"github.com/0xUnixIO/Xray-core/features/routing"
	"github.com/0xUnixIO/Xray-core/transport"
	"github.com/0xUnixIO/Xray-core/transport/pipe"
)

// 有流量的流必须活过 connIdle。计时器只在启动时武装、转发中不刷新的话，
// 大约一个 connIdle 就会把还在传数据的流取消。
func TestHandleStreamSurvivesTrafficPastIdle(t *testing.T) {
	idle := 250 * time.Millisecond
	aliveFor := 900 * time.Millisecond

	client, done := startStream(t, idle)
	payload := []byte("abcdefghij")
	deadline := time.Now().Add(aliveFor)
	for time.Now().Before(deadline) {
		if _, err := client.Write(payload); err != nil {
			t.Fatalf("流量未停，流在 connIdle(%s) 内被掐断: %v", idle, err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("客户端关闭后流没有结束")
	}
}

// 双向都没有后续字节时，流应在 connIdle 后结束。
func TestHandleStreamClosesWhenIdle(t *testing.T) {
	idle := 250 * time.Millisecond
	_, done := startStream(t, idle)
	select {
	case <-done:
		t.Fatal("还没到空闲时间，流就被关掉了")
	case <-time.After(idle / 2):
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("空闲流没有在 connIdle(%s) 后结束", idle)
	}
}

func startStream(t *testing.T, idle time.Duration) (net.Conn, <-chan struct{}) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	pad := &satomic.TypedValue[*anytlspadding.PaddingFactory]{}
	if !anytlspadding.UpdatePaddingScheme([]byte(defaultPaddingScheme), pad) {
		t.Fatal("padding scheme")
	}
	log := logger.NOP()

	done := make(chan struct{})
	sessionPolicy := policy.SessionDefault()
	sessionPolicy.Timeouts.ConnectionIdle = idle
	sessionPolicy.Timeouts.UplinkOnly = time.Hour
	sessionPolicy.Timeouts.DownlinkOnly = time.Hour

	serverSession := anytlssession.NewServerSession(serverConn, func(stream *anytlssession.Stream) {
		(&Server{}).handleStream(context.Background(), &memoryUser{email: "user@anytls"}, sessionPolicy, stream, &echoDispatcher{})
		close(done)
	}, pad, log)
	go serverSession.Run()

	// 客户端 Run 会先把 settings 放进缓冲再返回。必须等它返回后再开流，
	// 否则 SYN 会抢在 settings 前面发出，服务端直接关掉连接。
	clientSession := anytlssession.NewClientSession(clientConn, pad, log)
	clientSession.Run()

	stream, err := clientSession.OpenStream()
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := M.SocksaddrSerializer.WriteAddrPort(stream, M.ParseSocksaddrHostPort("1.1.1.1", 443)); err != nil {
		t.Fatalf("write dest: %v", err)
	}
	return stream, done
}

type echoDispatcher struct{}

func (d *echoDispatcher) Dispatch(ctx context.Context, dest xnet.Destination) (*transport.Link, error) {
	uplinkReader, uplinkWriter := pipe.New(pipe.WithoutSizeLimit())
	downlinkReader, downlinkWriter := pipe.New(pipe.WithoutSizeLimit())
	go func() {
		_ = buf.Copy(uplinkReader, downlinkWriter)
		_ = common.Close(downlinkWriter)
	}()
	return &transport.Link{Reader: downlinkReader, Writer: uplinkWriter}, nil
}

func (d *echoDispatcher) DispatchLink(context.Context, xnet.Destination, *transport.Link) error {
	return nil
}

func (d *echoDispatcher) Start() error { return nil }
func (d *echoDispatcher) Close() error { return nil }
func (d *echoDispatcher) Type() interface{} {
	return routing.DispatcherType()
}
