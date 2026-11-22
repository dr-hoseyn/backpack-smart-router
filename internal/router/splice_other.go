//go:build !linux

package router

import "net"

func copyTCP(_ *net.TCPConn, src *net.TCPConn, writer meteredWriter) error {
	return copyTCPBuffered(src, writer)
}
