package mrtelemetry

import (
	"io"
	"os"
)

// readCloser is the minimal interface summarize's file seam needs.
type readCloser interface {
	io.Reader
	io.Closer
}

// openFile is the default file opener used by the osOpen seam.
func openFile(path string) (readCloser, error) {
	return os.Open(path)
}

// ReadFile opens a JSONL file and decodes all attempt records from it.
// Best-effort: returns the valid records and any read error. A missing file
// yields (nil, nil).
func ReadFile(path string) ([]AttemptRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return ReadAll(f)
}
