package router

import (
	"errors"
	"io"
	"net"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	maxSplicePipes = 512
	spliceBatch    = 64 * 1024
	bulkReadSize   = 16 * 1024
	bulkReadCount  = 2
)

type splicePipe [2]int

var splicePool struct {
	sync.Mutex
	open int
	idle []*splicePipe
}

func borrowSplicePipe() *splicePipe {
	splicePool.Lock()
	if n := len(splicePool.idle); n > 0 {
		pipe := splicePool.idle[n-1]
		splicePool.idle = splicePool.idle[:n-1]
		splicePool.Unlock()
		return pipe
	}
	if splicePool.open == maxSplicePipes {
		splicePool.Unlock()
		return nil
	}
	splicePool.open++
	splicePool.Unlock()

	pipe := new(splicePipe)
	if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		splicePool.Lock()
		splicePool.open--
		splicePool.Unlock()
		return nil
	}
	// Linux may keep the smaller default when the process lacks pipe quota.
	_, _ = unix.FcntlInt(uintptr(pipe[1]), unix.F_SETPIPE_SZ, spliceBatch)
	return pipe
}

func releaseSplicePipe(pipe *splicePipe, reusable bool) {
	if !reusable {
		_ = unix.Close(pipe[0])
		_ = unix.Close(pipe[1])
	}
	splicePool.Lock()
	defer splicePool.Unlock()
	if reusable {
		splicePool.idle = append(splicePool.idle, pipe)
	} else {
		splicePool.open--
	}
}

func spliceBytes(from, to, count int) (int, error) {
	for {
		n, err := unix.Splice(from, nil, to, nil, count, unix.SPLICE_F_NONBLOCK)
		if !errors.Is(err, unix.EINTR) {
			return int(n), err
		}
	}
}

func spliceUnsupported(err error) bool {
	return err == unix.EINVAL || err == unix.ENOSYS || err == unix.EOPNOTSUPP || err == unix.EPERM
}

func copyTCP(dst, src *net.TCPConn, meter meteredWriter) error {
	bulk, err := copyTCPUntilBulk(src, meter)
	if !bulk {
		return err
	}
	if handled, err := spliceTCP(dst, src, meter); handled {
		return err
	}
	return copyTCPBuffered(src, meter)
}

// Short messages use a buffered copy. A waiting reader returns its buffer
// to the pool, and sustained large reads switch to the kernel copy path.
func copyTCPUntilBulk(src *net.TCPConn, meter meteredWriter) (bool, error) {
	input, err := src.SyscallConn()
	if err != nil {
		return false, copyTCPBuffered(src, meter)
	}

	var buffer *[32 * 1024]byte
	var count, consecutive int
	var readErr error
	defer func() {
		if buffer != nil {
			tcpBuffers.Put(buffer)
		}
	}()
	read := func(fd uintptr) bool {
		if buffer == nil {
			buffer = tcpBuffers.Get().(*[32 * 1024]byte)
		}
		for {
			count, readErr = unix.Read(int(fd), buffer[:])
			if readErr != unix.EINTR {
				break
			}
		}
		if readErr == unix.EAGAIN {
			tcpBuffers.Put(buffer)
			buffer = nil
			return false
		}
		return true
	}

	for {
		if err := input.Read(read); err != nil {
			return false, err
		}
		if readErr != nil || count == 0 {
			return false, readErr
		}
		written, err := meter.Write(buffer[:count])
		if err != nil {
			return false, err
		}
		if written != count {
			return false, io.ErrShortWrite
		}
		if count >= bulkReadSize {
			consecutive++
			if consecutive == bulkReadCount {
				return true, nil
			}
		} else {
			consecutive = 0
		}
	}
}

// spliceTCP returns handled=false only when the buffered path can resume at
// the next unread byte. Pending bytes are never returned to the pipe pool.
func spliceTCP(dst, src *net.TCPConn, meter meteredWriter) (bool, error) {
	input, err := src.SyscallConn()
	if err != nil {
		return false, nil
	}
	output, err := dst.SyscallConn()
	if err != nil {
		return false, nil
	}

	var pipe *splicePipe
	var pending int
	var spliceErr error
	defer func() {
		if pipe != nil {
			releaseSplicePipe(pipe, pending == 0)
		}
	}()

	read := func(fd uintptr) bool {
		if pipe == nil {
			pipe = borrowSplicePipe()
			if pipe == nil {
				return true
			}
		}
		pending, spliceErr = spliceBytes(int(fd), pipe[1], spliceBatch)
		if pending < 0 {
			pending = 0
		}
		if spliceErr == unix.EAGAIN {
			// A direction waiting for its next packet should not hold a pipe.
			releaseSplicePipe(pipe, true)
			pipe = nil
			return false
		}
		return true
	}

	write := func(fd uintptr) bool {
		for pending > 0 {
			n, err := spliceBytes(pipe[0], int(fd), pending)
			spliceErr = err
			if n > 0 {
				pending -= n
				meter.record(n)
			}
			if err != nil {
				return err != unix.EAGAIN
			}
			if n == 0 {
				spliceErr = io.ErrNoProgress
				return true
			}
		}
		return true
	}

	for {
		spliceErr = nil
		if err := input.Read(read); err != nil {
			return true, err
		}
		if pipe == nil || spliceUnsupported(spliceErr) {
			return false, nil
		}
		if spliceErr != nil || pending == 0 {
			return true, spliceErr
		}

		if err := output.Write(write); err != nil {
			return true, err
		}
		if spliceUnsupported(spliceErr) {
			// The source already advanced. Deliver the pipe contents before
			// letting the buffered path resume reading from the source.
			var err error
			pending, err = flushSplicePipe(pipe, pending, meter)
			return err != nil, err
		}
		if spliceErr != nil {
			return true, spliceErr
		}
	}
}

func flushSplicePipe(pipe *splicePipe, pending int, meter meteredWriter) (int, error) {
	buffer := tcpBuffers.Get().(*[32 * 1024]byte)
	defer tcpBuffers.Put(buffer)
	for pending > 0 {
		n, err := unix.Read(pipe[0], buffer[:min(pending, len(buffer))])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return pending, err
		}
		if n == 0 {
			return pending, io.ErrUnexpectedEOF
		}
		pending -= n
		written, err := meter.Write(buffer[:n])
		if err != nil {
			return pending, err
		}
		if written != n {
			return pending, io.ErrShortWrite
		}
	}
	return 0, nil
}
