//go:build !linux

package router

import "net"

func spliceTCP(_, _ *net.TCPConn, _ meteredWriter) (bool, error) {
	return false, nil
}
