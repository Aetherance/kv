package protocol

import "net"

type Command struct {
	Name string
	Args []string
}

type Response struct {
	Data interface{}
	Err error
}

type Protocol interface {
	// This method supports the parse of request
	ParseRequest(conn net.Conn) (Command,error)

	// This method encode response to bytes
	EncodeResponse(resp Response) []byte
}