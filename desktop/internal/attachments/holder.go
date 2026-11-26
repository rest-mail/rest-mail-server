package attachments

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const DefaultLimit = 25 << 20

type Metadata struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type Holder interface {
	Add(string, []string) ([]Metadata, error)
	Remove(string, string) []Metadata
	Clear(string)
}

type heldFile struct {
	metadata Metadata
	data     []byte
}

type Memory struct {
	mutex    sync.Mutex
	limit    int64
	sequence uint64
	drafts   map[string][]heldFile
}

func New(limit int64) *Memory {
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Memory{limit: limit, drafts: make(map[string][]heldFile)}
}

func metadata(files []heldFile) []Metadata {
	result := make([]Metadata, 0, len(files))
	for _, file := range files {
		result = append(result, file.metadata)
	}
	return result
}

func (holder *Memory) Add(draft string, paths []string) ([]Metadata, error) {
	holder.mutex.Lock()
	defer holder.mutex.Unlock()
	if draft == "" || len(paths) == 0 {
		return nil, errors.New("choose files to attach")
	}
	files := holder.drafts[draft]
	var total int64
	for _, file := range files {
		total += file.metadata.Size
	}
	pending := make([]heldFile, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("could not inspect %s", filepath.Base(path))
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file; folders cannot be attached", filepath.Base(path))
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("could not read %s", filepath.Base(path))
		}
		info, err = file.Stat()
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("could not inspect %s", filepath.Base(path))
		}
		if !info.Mode().IsRegular() {
			file.Close()
			return nil, fmt.Errorf("%s is not a regular file; folders cannot be attached", filepath.Base(path))
		}
		remaining := holder.limit - total
		if info.Size() > remaining {
			file.Close()
			return nil, fmt.Errorf("attachments exceed the %d MiB total limit", holder.limit>>20)
		}
		data, err := io.ReadAll(io.LimitReader(file, remaining+1))
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("could not read %s", filepath.Base(path))
		}
		if int64(len(data)) > remaining {
			return nil, fmt.Errorf("attachments exceed the %d MiB total limit", holder.limit>>20)
		}
		total += int64(len(data))
		holder.sequence++
		pending = append(pending, heldFile{metadata: Metadata{ID: strconv.FormatUint(holder.sequence, 10), Filename: filepath.Base(path), Size: int64(len(data))}, data: data})
	}
	holder.drafts[draft] = append(files, pending...)
	return metadata(holder.drafts[draft]), nil
}

func (holder *Memory) Remove(draft, identifier string) []Metadata {
	holder.mutex.Lock()
	defer holder.mutex.Unlock()
	files := holder.drafts[draft]
	for index, file := range files {
		if file.metadata.ID == identifier {
			files = append(files[:index:index], files[index+1:]...)
			break
		}
	}
	holder.drafts[draft] = files
	return metadata(files)
}

func (holder *Memory) Clear(draft string) {
	holder.mutex.Lock()
	defer holder.mutex.Unlock()
	delete(holder.drafts, draft)
}
