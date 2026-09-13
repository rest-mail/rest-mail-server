package mime

import (
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
)

// TextAndHTML walks a parsed body tree and returns the first text/plain and the
// first text/html content found, at any depth.
//
// The gateways need exactly these two fields to build a DeliverRequest, and a
// message may nest them arbitrarily: a text/plain inside a multipart/alternative
// inside a multipart/mixed alongside an attachment is an ordinary shape. Walking
// the tree here keeps that knowledge in one place instead of a copy per gateway.
func TextAndHTML(body pipeline.Body) (text, html string) {
	var walk func(pipeline.Body)
	walk = func(node pipeline.Body) {
		switch normalizeMediaType(node.ContentType) {
		case "text/plain":
			if text == "" {
				text = node.Content
			}
		case "text/html":
			if html == "" {
				html = node.Content
			}
		}
		for _, part := range node.Parts {
			walk(part)
		}
	}
	walk(body)
	return text, html
}

// normalizeMediaType strips any parameters from a content type and lower-cases
// what remains, so "TEXT/HTML; charset=utf-8" matches "text/html".
func normalizeMediaType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}
