package cascade

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// ErrNotRegular is matched (errors.Is) by ReadRegularFile's error for a path
// that opened but is not a regular file: a FIFO, a device, a directory.
var ErrNotRegular = errors.New("not a regular file")

// notRegularError names the file type, e.g. "not a regular file (p---------)".
type notRegularError struct{ mode fs.FileMode }

func (e notRegularError) Error() string {
	return fmt.Sprintf("%s (%s)", ErrNotRegular, e.mode.Type())
}

func (e notRegularError) Is(target error) bool { return target == ErrNotRegular }

// ReadRegularFile is the one reader of the session-writable launch-config
// files — each cascade config.yaml and env file (CS-CASC-047, CS-LNCH-172) —
// and of the child Dockerfile and its ignore file (CS-IMG-074). The project
// tree is mounted read-write, so a session can put a FIFO where one of these
// files belongs; os.ReadFile would then block the next launch forever in
// open(2) (no writer) or in read(2). So the file is opened O_NONBLOCK (a FIFO
// opens at once) and O_NOCTTY (a terminal device never becomes the
// launcher's controlling terminal), fstat-checked on the opened descriptor,
// and read only when it is a regular file. Symlinks are followed, as
// os.ReadFile follows them: the checks apply to what the link names.
//
// An open, stat or read failure is returned as is (an *fs.PathError naming
// the path; errors.Is(err, fs.ErrNotExist) works). A file that is not
// regular is an *fs.PathError naming the path whose Err matches
// ErrNotRegular. An empty regular file yields a non-nil empty slice.
func ReadRegularFile(path string) ([]byte, error) {
	fh, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "open", Path: path, Err: notRegularError{fi.Mode()}}
	}
	raw, err := io.ReadAll(fh)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		raw = []byte{}
	}
	return raw, nil
}
