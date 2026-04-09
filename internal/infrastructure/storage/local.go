package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Storage interface {
	Writer(exportID, extension string) (io.WriteCloser, error)
	Reader(exportID, extension string) (io.ReadCloser, error)
	Delete(exportID, extension string) error
	Size(exportID, extension string) (int64, error)
	FilePath(exportID, extension string) string
}

var ErrFileNotFound = fmt.Errorf("storage: file not found")

type LocalStorage struct {
	baseDir string
}

func NewLocalStorage(baseDir string) (*LocalStorage, error) {
	absDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("storage: resolving base dir %q: %w", baseDir, err)
	}
	if err := os.MkdirAll(absDir, 0755); err != nil {
		return nil, fmt.Errorf("storage: creating base dir %q: %w", absDir, err)
	}
	return &LocalStorage{baseDir: absDir}, nil
}

func (s *LocalStorage) Writer(exportID, extension string) (io.WriteCloser, error) {
	f, err := os.Create(s.FilePath(exportID, extension))
	if err != nil {
		return nil, fmt.Errorf("storage: creating file: %w", err)
	}
	return f, nil
}

func (s *LocalStorage) Reader(exportID, extension string) (io.ReadCloser, error) {
	f, err := os.Open(s.FilePath(exportID, extension))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("storage: opening file: %w", err)
	}
	return f, nil
}

func (s *LocalStorage) Delete(exportID, extension string) error {
	if err := os.Remove(s.FilePath(exportID, extension)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("storage: deleting file: %w", err)
	}
	return nil
}

func (s *LocalStorage) Size(exportID, extension string) (int64, error) {
	info, err := os.Stat(s.FilePath(exportID, extension))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("storage: stat file: %w", err)
	}
	return info.Size(), nil
}

func (s *LocalStorage) FilePath(exportID, extension string) string {
	return filepath.Join(s.baseDir, exportID+extension)
}
