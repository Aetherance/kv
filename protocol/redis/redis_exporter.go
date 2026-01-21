package redis_protocol

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"

	"github.com/Aetherance/kv/coord"
)

type RedisExporter struct {
	protocol *RedisProtocol
	l        net.Listener
}

func NewExporter() *RedisExporter {
	return &RedisExporter{
		protocol: New(),
	}
}

// This method will block whole thread, should run in a seprate thread
func (re *RedisExporter) Export(ctx context.Context, c coord.Coordinator, addr string) error {
	var err error
	re.l, err = net.Listen("tcp", addr)

	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		re.Stop(ctx)
	}()

	for {
		conn, err := re.l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			} else {
				log.Println(err)
				continue
			}
		}
		go re.handler(conn, c, ctx)
	}
}

func (re *RedisExporter) handler(conn net.Conn,
	coord coord.Coordinator,
	ctx context.Context) {
	defer conn.Close()

	reader := bufio.NewReader(conn)

	for {
		req, err := re.protocol.ParseRequest(reader)
		if err != nil {
			if err == io.EOF {
				return
			} else {
				continue
			}
		}

		resp := coord.Coordinator(ctx, req)
		data := re.protocol.EncodeResponse(resp)

		_, _ = conn.Write(data)
	}
}

func (re *RedisExporter) Stop(ctx context.Context) error {
	if re.l != nil {
		return re.l.Close()
	}
	return errors.New("Redis server stop but no listener created")
}