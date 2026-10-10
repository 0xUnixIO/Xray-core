package singbridge

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/0xUnixIO/Xray-core/common/buf"
	"github.com/0xUnixIO/Xray-core/features/policy"
	"github.com/0xUnixIO/Xray-core/transport"
	"github.com/0xUnixIO/Xray-core/transport/pipe"
)

func TestCopyConnEndsWhenIdle(t *testing.T) {
	idle := 200 * time.Millisecond
	ctx := contextWithTimeouts(context.Background(), policy.Timeout{
		ConnectionIdle: idle,
		UplinkOnly:     time.Second,
		DownlinkOnly:   time.Second,
	})
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	link := newIdleLink()
	errCh := make(chan error, 1)
	go func() {
		errCh <- CopyConn(ctx, nil, link, server)
	}()
	select {
	case <-errCh:
		t.Fatal("还没到空闲时间，连接就被关掉了")
	case <-time.After(idle / 2):
	}
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("两边都没有流量时，连接没有在 connIdle(%s) 后结束", idle)
	}
}

func TestCopyConnStaysUpWhileOneDirectionFlows(t *testing.T) {
	idle := 200 * time.Millisecond
	ctx := contextWithTimeouts(context.Background(), policy.Timeout{
		ConnectionIdle: idle,
		UplinkOnly:     time.Second,
		DownlinkOnly:   time.Second,
	})
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	go func() { _, _ = io.Copy(io.Discard, client) }()

	uplinkReader, uplinkWriter := pipe.New(pipe.WithoutSizeLimit())
	downlinkReader, downlinkWriter := pipe.New(pipe.WithoutSizeLimit())
	go func() { _ = buf.Copy(downlinkReader, buf.Discard) }()
	link := &transport.Link{Reader: uplinkReader, Writer: downlinkWriter}

	errCh := make(chan error, 1)
	go func() {
		errCh <- CopyConn(ctx, nil, link, server)
	}()

	deadline := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(deadline) {
		b := buf.New()
		b.Write([]byte("hi"))
		if err := uplinkWriter.WriteMultiBuffer(buf.MultiBuffer{b}); err != nil {
			t.Fatalf("上行仍在写，转发已结束: %v", err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	select {
	case err := <-errCh:
		t.Fatalf("另一方向持续有流量，连接仍在 connIdle(%s) 内被掐断: %v", idle, err)
	default:
	}
	_ = uplinkWriter.Close()
}

func newIdleLink() *transport.Link {
	uplinkReader, _ := pipe.New(pipe.WithoutSizeLimit())
	_, downlinkWriter := pipe.New(pipe.WithoutSizeLimit())
	return &transport.Link{Reader: uplinkReader, Writer: downlinkWriter}
}
