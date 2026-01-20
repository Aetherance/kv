package redis_protocol

import (
	"bufio"
	"net"
	"strings"

	"github.com/Aetherance/kv/protocol"
)

type RedisProtocol struct{}

func New() *RedisProtocol {
	return &RedisProtocol{}
}

func (r *RedisProtocol) ParseRequest(conn net.Conn) (protocol.Command,error) {
	reader := bufio.NewReader(conn)

	args, err := parseArray(reader)
	if err != nil {
		return protocol.Command{}, err
	}

	return protocol.Command{
		Name: strings.ToUpper(args[0]),
		Args: args[1:],
	},nil
}

func (r *RedisProtocol) EncodeResponse(resp protocol.Response) []byte {
	return encode(resp)
}