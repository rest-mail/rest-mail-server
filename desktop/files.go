package main

import (
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const maxAttachmentBytes = 25 << 20

type Files struct{}

type PickedAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Content     string `json:"content"`
}

func (f *Files) PickAttachments() ([]PickedAttachment, error) {
	paths, err := application.Get().Dialog.OpenFile().CanChooseFiles(true).CanChooseDirectories(false).SetTitle("Attach files").PromptForMultipleSelection()
	if err != nil {
		return nil, err
	}
	attachments := make([]PickedAttachment, 0, len(paths))
	total := 0
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
		file.Close()
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > maxAttachmentBytes {
			return nil, errors.New("attachments exceed the 25 MiB selection limit")
		}
		contentType := mime.TypeByExtension(filepath.Ext(path))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		attachments = append(attachments, PickedAttachment{Filename: filepath.Base(path), ContentType: contentType, Content: base64.StdEncoding.EncodeToString(data)})
	}
	return attachments, nil
}

func (f *Files) SaveAttachment(filename, content string) error {
	if len(content) > base64.StdEncoding.EncodedLen(maxAttachmentBytes) {
		return errors.New("attachment exceeds the 25 MiB download limit")
	}
	data, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return err
	}
	path, err := application.Get().Dialog.SaveFile().SetFilename(filepath.Base(filename)).SetMessage("Save attachment").PromptForSingleSelection()
	if err != nil || path == "" {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
