package service

import (
	"errors"
	"io"
	"os"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// uploadPeerFS serves real SFTP OPEN/WRITE/FSETSTAT/CLOSE requests. Tests inject
// failures at protocol boundaries rather than emulating shell upload commands.
type uploadPeerFS struct {
	open       func(string) error
	write      func()
	writeError func(int64) error
	finish     func(string, []byte) error
}

func (fs *uploadPeerFS) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	if fs.open != nil {
		if err := fs.open(r.Filepath); err != nil {
			return nil, err
		}
	}
	return &uploadPeerFile{path: r.Filepath, finish: fs.finish, write: fs.write, writeError: fs.writeError}, nil
}
func (fs *uploadPeerFS) Filecmd(r *sftp.Request) error {
	if r.Method == "Setstat" {
		return nil
	}
	return os.ErrInvalid
}

type uploadPeerFile struct {
	write      func()
	writeError func(int64) error
	mu         sync.Mutex
	path       string
	data       []byte
	closed     bool
	closeErr   error
	finish     func(string, []byte) error
}

func (f *uploadPeerFile) WriteAt(p []byte, offset int64) (int, error) {
	if f.write != nil {
		f.write()
	}
	if f.writeError != nil {
		if err := f.writeError(offset); err != nil {
			return 0, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if offset < 0 || offset+int64(len(p)) > 128<<20 {
		return 0, errors.New("test upload exceeds bound")
	}
	end := int(offset) + len(p)
	if end > len(f.data) {
		f.data = append(f.data, make([]byte, end-len(f.data))...)
	}
	return copy(f.data[int(offset):], p), nil
}
func (f *uploadPeerFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		if f.finish != nil {
			f.closeErr = f.finish(f.path, append([]byte(nil), f.data...))
		}
	}
	return f.closeErr
}
func serveUploadPeer(ch ssh.Channel, fs *uploadPeerFS) {
	defer ch.Close()
	server := sftp.NewRequestServer(ch, sftp.Handlers{FilePut: fs, FileCmd: fs})
	_ = server.Serve()
	_ = server.Close()
}
