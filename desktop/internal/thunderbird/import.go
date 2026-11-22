package thunderbird

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	shared "github.com/rest-mail/rest-mail-server/pkg/mailimport"
)

type Selection struct {
	ProfileID string   `json:"profile_id"`
	Folders   []string `json:"folders"`
	AccountID uint32   `json:"account_id"`
}

type Failure struct {
	Folder  string `json:"folder"`
	Message int64  `json:"message,omitempty"`
	Reason  string `json:"reason"`
}

type Status struct {
	State         string    `json:"state"`
	Selection     Selection `json:"selection"`
	Folder        string    `json:"folder"`
	Messages      int64     `json:"messages"`
	TotalMessages int64     `json:"total_messages"`
	Bytes         int64     `json:"bytes"`
	TotalBytes    int64     `json:"total_bytes"`
	Imported      int64     `json:"imported"`
	Duplicates    int64     `json:"duplicates"`
	Expunged      int64     `json:"expunged"`
	Failed        int64     `json:"failed"`
	Failures      []Failure `json:"failures"`
	Session       string    `json:"session,omitempty"`
	Updated       time.Time `json:"updated"`
}

type Service struct {
	Prepare      func(uint32) (shared.Request, error)
	mu           sync.Mutex
	request      shared.Request
	progressFile string
	catalog      Catalog
	discovered   bool
	discover     func() ([]Profile, error)
	running      func() bool
	status       Status
	cancel       context.CancelFunc
	ctx          context.Context
	stop         context.CancelFunc
	wait         sync.WaitGroup
	lastSave     time.Time
}

func New(request shared.Request, progressFile string) *Service {
	ctx, stop := context.WithCancel(context.Background())
	service := &Service{request: request, progressFile: progressFile, discover: Discover, running: thunderbirdRunning, ctx: ctx, stop: stop, status: Status{State: "idle", Failures: []Failure{}}, catalog: Catalog{Profiles: []Profile{}}}
	if progressFile != "" {
		file, err := os.Open(progressFile)
		if err == nil {
			var saved Status
			if json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&saved) == nil {
				if saved.State == "running" || saved.State == "cancelling" {
					saved.State = "interrupted"
				}
				service.status = saved
			}
			file.Close()
		}
	}
	return service
}

func (service *Service) Close() {
	service.stop()
	service.wait.Wait()
}

func (service *Service) Profiles() Catalog {
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.discovered {
		service.discovered = true
		profiles, err := service.discover()
		service.catalog.Running = service.running()
		if err != nil {
			service.catalog.Problem = "Could not read Thunderbird profiles. Check file permissions."
		} else {
			owned := append([]Profile{}, profiles...)
			for index := range owned {
				owned[index].Folders = append([]Folder{}, profiles[index].Folders...)
			}
			service.catalog.Profiles = owned
			service.catalog.Scanning = true
			service.wait.Add(1)
			go service.scan()
		}
	}
	data, _ := json.Marshal(service.catalog)
	var result Catalog
	_ = json.Unmarshal(data, &result)
	return result
}

func (service *Service) RefreshProfiles() Catalog {
	service.mu.Lock()
	if !service.catalog.Scanning && service.cancel == nil {
		service.discovered = false
	}
	service.mu.Unlock()
	return service.Profiles()
}

func (service *Service) scan() {
	defer service.wait.Done()
	defer func() { service.mu.Lock(); service.catalog.Scanning = false; service.mu.Unlock() }()
	for profileIndex, profile := range service.catalog.Profiles {
		root, err := os.OpenRoot(profile.path)
		if err != nil {
			continue
		}
		for folderIndex, folder := range profile.Folders {
			if service.ctx.Err() != nil {
				break
			}
			var messages, expunged int64
			file, openErr := openFolder(root, folder.ID)
			if openErr == nil {
				messages, expunged, openErr = countMessages(service.ctx, file)
				file.Close()
			}
			service.mu.Lock()
			entry := &service.catalog.Profiles[profileIndex].Folders[folderIndex]
			if openErr != nil {
				entry.Problem = "Could not count messages; folder may be incomplete or inaccessible."
			} else {
				entry.Messages, entry.Expunged = messages, expunged
			}
			service.mu.Unlock()
		}
		root.Close()
	}
}

func openFolder(root *os.Root, id string) (*os.File, error) {
	path := filepath.FromSlash(id)
	if !filepath.IsLocal(path) {
		return nil, errors.New("invalid source folder")
	}
	parts := strings.Split(path, string(filepath.Separator))
	for index := range parts {
		info, err := root.Lstat(filepath.Join(parts[:index+1]...))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("source symlinks are not supported")
		}
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, errors.New("could not open source folder")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("source folder is not a regular file")
	}
	return file, nil
}

func (service *Service) Start(selection Selection) (Status, error) {
	selection.Folders = append([]string{}, selection.Folders...)
	service.Profiles()
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.cancel != nil {
		return Status{}, errors.New("an import is already running")
	}
	if service.ctx.Err() != nil {
		return Status{}, errors.New("import service is shutting down")
	}
	if selection.AccountID == 0 || len(selection.Folders) == 0 {
		return Status{}, errors.New("choose source folders and a REST Mail account")
	}
	var profile Profile
	for _, candidate := range service.catalog.Profiles {
		if candidate.ID == selection.ProfileID {
			profile = candidate
			break
		}
	}
	if profile.path == "" {
		return Status{}, errors.New("unknown Thunderbird profile")
	}
	folders := []Folder{}
	seen := map[string]bool{}
	var totalBytes, totalMessages int64
	for _, id := range selection.Folders {
		if seen[id] {
			continue
		}
		seen[id] = true
		found := false
		for _, folder := range profile.Folders {
			if folder.ID == id {
				folders = append(folders, folder)
				totalBytes += folder.Bytes
				totalMessages += max(0, folder.Messages)
				found = true
				break
			}
		}
		if !found {
			return Status{}, errors.New("unknown Thunderbird folder")
		}
	}
	ctx, cancel := context.WithCancel(service.ctx)
	request := service.request
	if service.Prepare != nil {
		var err error
		request, err = service.Prepare(selection.AccountID)
		if err != nil {
			cancel()
			return Status{}, err
		}
	}
	service.cancel = cancel
	service.status = Status{State: "running", Selection: selection, TotalBytes: totalBytes, TotalMessages: totalMessages, Failures: []Failure{}}
	if err := service.saveLocked(true); err != nil {
		cancel()
		service.cancel = nil
		service.status.State = "failed"
		return Status{}, err
	}
	service.wait.Add(1)
	go service.run(ctx, profile.path, folders, selection.AccountID, request)
	return service.snapshotLocked(), nil
}

func (service *Service) Status(cancel bool) Status {
	service.mu.Lock()
	defer service.mu.Unlock()
	if cancel && service.cancel != nil {
		service.cancel()
		service.status.State = "cancelling"
	}
	return service.snapshotLocked()
}

func (service *Service) snapshotLocked() Status {
	result := service.status
	result.Selection.Folders = append([]string{}, result.Selection.Folders...)
	result.Failures = append([]Failure{}, result.Failures...)
	return result
}

func (service *Service) saveLocked(force bool) error {
	service.status.Updated = time.Now().UTC()
	if service.progressFile == "" || !force && time.Since(service.lastSave) < time.Second {
		return nil
	}
	data, err := json.Marshal(service.status)
	if err != nil {
		return errors.New("could not encode import progress")
	}
	if os.MkdirAll(filepath.Dir(service.progressFile), 0700) != nil {
		return errors.New("could not create import progress directory")
	}
	file, err := os.CreateTemp(filepath.Dir(service.progressFile), ".import-progress-*")
	if err != nil {
		return errors.New("could not save import progress")
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Close()
	} else {
		file.Close()
	}
	if err == nil {
		err = os.Rename(name, service.progressFile)
	}
	if err != nil {
		return errors.New("could not save import progress")
	}
	service.lastSave = time.Now()
	return nil
}

func (service *Service) failure(folder string, ordinal int64, reason string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.status.Failed++
	if len(service.status.Failures) < 100 {
		service.status.Failures = append(service.status.Failures, Failure{Folder: folder, Message: ordinal, Reason: reason})
	}
}

type progressReader struct {
	reader   io.Reader
	progress func(int64)
}

func (reader *progressReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.progress(int64(count))
	return count, err
}

func (service *Service) run(ctx context.Context, profilePath string, folders []Folder, account uint32, request shared.Request) {
	defer service.wait.Done()
	defer func() {
		service.mu.Lock()
		defer service.mu.Unlock()
		service.cancel = nil
		service.status.State = "completed"
		if ctx.Err() != nil {
			service.status.State = "cancelled"
		} else if service.status.Failed > 0 {
			service.status.State = "failed"
		}
		if service.saveLocked(true) != nil {
			service.status.State = "failed"
			service.status.Failed++
			service.status.Failures = append(service.status.Failures, Failure{Reason: "Could not persist final progress. Rerun safely using server deduplication."})
		}
	}()
	root, err := os.OpenRoot(profilePath)
	if err != nil {
		service.failure("", 0, "Could not open Thunderbird profile.")
		return
	}
	defer root.Close()
	session, err := shared.OpenSession(ctx, request, account, "REST Mail desktop Thunderbird import")
	if err != nil {
		if ctx.Err() == nil {
			service.failure("", 0, err.Error())
		}
		return
	}
	service.mu.Lock()
	service.status.Session = session
	service.mu.Unlock()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := shared.CloseSession(closeCtx, request, account, session); err != nil {
			service.failure("", 0, err.Error())
		}
	}()
	destinations := shared.NewDestinations()
	for _, folder := range folders {
		if ctx.Err() != nil {
			return
		}
		service.mu.Lock()
		service.status.Folder = folder.Name
		service.mu.Unlock()
		file, err := openFolder(root, folder.ID)
		if err != nil {
			service.failure(folder.Name, 0, err.Error())
			continue
		}
		info, err := file.Stat()
		if err != nil || info.Size() != folder.Bytes {
			file.Close()
			service.failure(folder.Name, 0, "Source changed after discovery. Quit Thunderbird and reopen the import sheet.")
			continue
		}
		var ordinal int64
		reader := &progressReader{reader: io.LimitReader(file, info.Size()), progress: func(count int64) {
			service.mu.Lock()
			service.status.Bytes += count
			_ = service.saveLocked(false)
			service.mu.Unlock()
		}}
		err = shared.Thunderbird(ctx, reader, folder.Destination, 32<<20, func(_ string, message shared.Message) error {
			ordinal++
			message.Folder = destinations.For(folder.Destination)
			if err := ctx.Err(); err != nil {
				return err
			}
			requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			result, status, uploadErr := shared.Post(requestCtx, request, account, session, message)
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			service.mu.Lock()
			service.status.Messages++
			service.mu.Unlock()
			if uploadErr != nil {
				service.failure(folder.Name, ordinal, uploadErr.Error())
				return nil
			}
			if status != http.StatusOK {
				service.failure(folder.Name, ordinal, fmt.Sprintf("Server refused message (HTTP %d). Check permission, size and quota.", status))
				return nil
			}
			if result.ID == 0 || result.MessageID == "" || !destinations.Record(folder.Destination, result.Folder) {
				service.failure(folder.Name, ordinal, "Server returned an invalid import result.")
				return nil
			}
			service.mu.Lock()
			if result.Imported {
				service.status.Imported++
			} else {
				service.status.Duplicates++
			}
			_ = service.saveLocked(false)
			service.mu.Unlock()
			return nil
		}, func() { ordinal++; service.mu.Lock(); service.status.Expunged++; service.mu.Unlock() })
		after, statErr := file.Stat()
		file.Close()
		if err != nil && ctx.Err() == nil {
			service.failure(folder.Name, ordinal+1, err.Error())
		}
		if statErr != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			service.failure(folder.Name, 0, "Source changed during import. Quit Thunderbird and rerun to verify every message.")
		}
	}
}
