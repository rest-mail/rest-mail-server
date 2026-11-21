package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNativeSaveRejectsInvalidDataBeforeOpeningDialog(t *testing.T) {
	files := &Files{}
	if err := files.SaveAttachment("test.txt", "not-base64!"); err == nil {
		t.Fatal("invalid base64 was accepted")
	}
}

func TestNativeSaveRejectsOversizedDataBeforeOpeningDialog(t *testing.T) {
	files := &Files{}
	if err := files.SaveAttachment("test.txt", strings.Repeat("A", base64.StdEncoding.EncodedLen(maxAttachmentBytes)+1)); err == nil {
		t.Fatal("oversized attachment was accepted")
	}
}
