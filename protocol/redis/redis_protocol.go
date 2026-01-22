package redis_protocol

import (
	"bufio"
	"errors"
	"strings"

	"github.com/Aetherance/kv/common"
)

type RedisProtocol struct{}

func New() *RedisProtocol {
	return &RedisProtocol{}
}

func (r *RedisProtocol) ParseRequest(reader *bufio.Reader) (*common.Request, error) {
	args, err := parseArray(reader)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("redis: empty command")
	}

	switch strings.ToUpper(args[0]) {
	case "GET":
		if len(args) != 2 {
			return nil, errors.New("redis: GET expects 1 argument")
		}
		return &common.Request{
			Op:  common.OpGet,
			Key: []byte(args[1]),
		}, nil
	case "SET":
		if len(args) != 3 {
			return nil, errors.New("redis: SET expects 2 arguments")
		}
		return &common.Request{
			Op:    common.OpSet,
			Key:   []byte(args[1]),
			Value: []byte(args[2]),
		}, nil
	case "DEL":
		if len(args) != 2 {
			return nil, errors.New("redis: DEL expects 1 argument")
		}
		return &common.Request{
			Op:  common.OpDel,
			Key: []byte(args[1]),
		}, nil
	case "PING":
		return &common.Request{
			Op: common.OpPing,
		}, nil
	case "COMMAND":
		return &common.Request{
			Op: common.OpCommand,
		}, nil
	default:
		return &common.Request{
			Op: common.OpUnknown,
		}, nil
	}
}

func (r *RedisProtocol) EncodeResponse(resp *common.Response) []byte {
	return encode(resp)
}
