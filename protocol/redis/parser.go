package redis_protocol

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func parseArray(reader *bufio.Reader) ([]string, error) {
	// read "*<n>\r\n"
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)

	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected array, got %s", line)
	}

	n, err := strconv.Atoi(line[1:])
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("invalid array length")
	}

	args := make([]string, 0, n)

	for i := 0; i < n; i++ {
		// read "$<len>\r\n"
		lenLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		lenLine = strings.TrimSpace(lenLine)

		if !strings.HasPrefix(lenLine, "$") {
			return nil, fmt.Errorf("expected bulk string")
		}

		l, _ := strconv.Atoi(lenLine[1:])

		// read "<data>\r\n"
		buf := make([]byte, l+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}

		args = append(args, string(buf[:l]))
	}

	return args, nil
}
