// Package attachmentstore holds private attachment objects. Authorization is
// checked by the API before it opens an object; it never issues public URLs.
package attachmentstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/restmail/restmail/internal/config"
)

type S3 struct {
	client *s3.Client
	bucket string
	prefix string
}

// NewS3 constructs a client from already validated deployment configuration.
// Filesystem deployments receive nil and retain their existing storage.
func NewS3(c config.AttachmentStorageConfig) *S3 {
	if c.Type != "s3" {
		return nil
	}
	client := s3.NewFromConfig(aws.Config{
		Region:      c.S3Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(c.S3AccessKeyID, c.S3SecretAccessKey, "")),
		HTTPClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		// Garage uses ordinary signed payloads. Avoid the SDK's optional
		// streaming-checksum extension, which is not part of every S3 store.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}, func(o *s3.Options) {
		if c.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(c.S3Endpoint)
		}
		o.UsePathStyle = c.S3ForcePathStyle
	})
	return &S3{client: client, bucket: c.S3Bucket, prefix: c.S3Prefix}
}

// Put is idempotent: identical bytes use the same full SHA-256 object key.
// Sender filenames never become object paths or access-control decisions.
func (s *S3) Put(ctx context.Context, data []byte) (ref, checksum string, err error) {
	return s.PutReader(ctx, bytes.NewReader(data), int64(len(data)))
}

// PutReader also supports migrating large existing files without buffering
// them in memory. The source must stay unchanged until the upload completes.
func (s *S3) PutReader(ctx context.Context, source io.ReadSeeker, size int64) (ref, checksum string, err error) {
	hash := sha256.New()
	if size < 0 {
		return "", "", fmt.Errorf("invalid attachment size")
	}
	n, err := io.Copy(hash, source)
	if err != nil || n != size {
		return "", "", fmt.Errorf("attachment source size changed or could not be read")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	checksum = hex.EncodeToString(hash.Sum(nil))
	ref = s.prefix + checksum
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(ref), Body: source,
		ContentLength: aws.Int64(size), ContentType: aws.String("application/octet-stream"),
	})
	return ref, checksum, err
}

func (s *S3) Delete(ctx context.Context, ref string) error {
	if !s.validRef(ref) {
		return fmt.Errorf("invalid attachment object reference")
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(ref)})
	return err
}

func (s *S3) validRef(ref string) bool {
	if !strings.HasPrefix(ref, s.prefix) {
		return false
	}
	suffix := strings.TrimPrefix(ref, s.prefix)
	decoded, err := hex.DecodeString(suffix)
	return err == nil && len(decoded) == sha256.Size && suffix == strings.ToLower(suffix)
}

// Reader implements ReadSeeker so net/http can serve conditional and range
// downloads. The bytes stream from S3; a download never buffers a whole object.
type Reader struct {
	store    *S3
	ctx      context.Context
	ref      string
	position int64
	body     io.ReadCloser
	Size     int64
	Modified time.Time
	ETag     string
}

func (s *S3) Open(ctx context.Context, ref string) (*Reader, error) {
	if !s.validRef(ref) {
		return nil, fmt.Errorf("invalid attachment object reference")
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(ref)})
	if err != nil {
		var response *smithyhttp.ResponseError
		if errors.As(err, &response) && response.HTTPStatusCode() == http.StatusNotFound {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	if head.ContentLength == nil || *head.ContentLength < 0 {
		return nil, fmt.Errorf("object store returned an invalid attachment size")
	}
	return &Reader{store: s, ctx: ctx, ref: ref, Size: *head.ContentLength, Modified: aws.ToTime(head.LastModified), ETag: aws.ToString(head.ETag)}, nil
}

func (r *Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.position >= r.Size {
		return 0, io.EOF
	}
	if r.body == nil {
		response, err := r.store.client.GetObject(r.ctx, &s3.GetObjectInput{
			Bucket: aws.String(r.store.bucket), Key: aws.String(r.ref),
			Range: aws.String(fmt.Sprintf("bytes=%d-", r.position)),
		})
		if err != nil {
			return 0, err
		}
		r.body = response.Body
	}
	n, err := r.body.Read(p)
	r.position += int64(n)
	if errors.Is(err, io.EOF) && r.position < r.Size {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	position := offset
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		position += r.position
	case io.SeekEnd:
		position += r.Size
	default:
		return 0, fmt.Errorf("invalid seek mode")
	}
	if position < 0 {
		return 0, fmt.Errorf("negative attachment offset")
	}
	if position != r.position {
		if err := r.Close(); err != nil {
			return 0, err
		}
		r.position = position
	}
	return r.position, nil
}

func (r *Reader) Close() error {
	if r.body == nil {
		return nil
	}
	err := r.body.Close()
	r.body = nil
	return err
}
