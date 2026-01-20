package redis_protocol

import (
	"strconv"
	"strings"

	"github.com/Aetherance/kv/protocol"
)

func encode(resp protocol.Response) []byte {
	if resp.Err != nil {
		return []byte("-ERR " + resp.Err.Error() + "\r\n")
	}

	switch v := resp.Data.(type) {
	case nil:
		return []byte("$-1\r\n")

	case string:
		return []byte("$" + strconv.Itoa(len(v)) + "\r\n" + v + "\r\n")

	case int:
		return []byte(":" + strconv.Itoa(v) + "\r\n")

	case []string:
		var b strings.Builder
		b.WriteString("*" + strconv.Itoa(len(v)) + "\r\n")
		for _, s := range v {
			b.WriteString("$" + strconv.Itoa(len(s)) + "\r\n")
			b.WriteString(s + "\r\n")
		}
		return []byte(b.String())

	default:
		return []byte("+OK\r\n")
	}
}