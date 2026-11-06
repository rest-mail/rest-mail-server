package auditanchor

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/tracing"
	"gorm.io/gorm"
)

const s3Action = "audit.anchor.s3"

// The object storage target keeps its own lock and schedule too.
const s3PublicationLock int64 = 0x6175647473336c6b // "audts3lk"

// s3Target writes each anchor as a new object in a bucket with S3 Object Lock.
// The bucket's default retention, set by the operator, keeps the object from
// being changed or deleted; the server never sets or shortens retention. A
// bucket without Object Lock and a default retention rule is refused, because
// an anchor there could be rewritten together with the log.
type s3Target struct {
	client         *s3.Client
	bucket, prefix string
	backoff        time.Duration
}

// put checks the bucket's Object Lock configuration and writes the object,
// up to httpsAttempts times with the same backoff as the HTTPS target.
// Network errors, 408, 429 and 5xx are retried; any other answer is final.
func (t *s3Target) put(ctx context.Context, key, body string) delivery {
	var d delivery
	wait := t.backoff
	for d.attempts < httpsAttempts {
		if d.attempts > 0 {
			select {
			case <-ctx.Done():
				return d
			case <-time.After(wait):
			}
			wait *= 2
		}
		d.attempts++
		var retry bool
		d, retry = t.attempt(ctx, d, key, body)
		if !retry {
			return d
		}
	}
	return d
}

func (t *s3Target) attempt(ctx context.Context, d delivery, key, body string) (delivery, bool) {
	lock, err := t.client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(t.bucket)})
	if err != nil {
		return s3Failure(d, err)
	}
	if c := lock.ObjectLockConfiguration; c == nil || c.ObjectLockEnabled != types.ObjectLockEnabledEnabled || c.Rule == nil || c.Rule.DefaultRetention == nil {
		d.result, d.status, d.err = "refused", 0, errors.New("bucket has no Object Lock default retention")
		return d, false
	}
	// Object Lock buckets require an integrity checksum on every write.
	sum := md5.Sum([]byte(body))
	_, err = t.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(t.bucket), Key: aws.String(key), Body: strings.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))), ContentType: aws.String("text/plain; charset=utf-8"),
		ContentMD5: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	})
	if err != nil {
		return s3Failure(d, err)
	}
	d.result, d.status, d.err = "accepted", 0, nil
	return d, false
}

// s3Failure classifies a failed request like the HTTPS target classifies an
// answer, and reports whether it is worth another attempt.
func s3Failure(d delivery, err error) (delivery, bool) {
	var response *smithyhttp.ResponseError
	// A connection failure can also arrive as a ResponseError, with no status.
	if !errors.As(err, &response) || response.Response == nil || response.HTTPStatusCode() == 0 {
		d.result, d.status, d.err = "unreachable", 0, err
		return d, true
	}
	status := response.HTTPStatusCode()
	d.result, d.status, d.err = "refused", status, err
	return d, status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

// S3Worker publishes the same anchors as the email Worker, on the same
// schedule, to an operator-configured bucket with Object Lock. Each attempt,
// accepted or not, is recorded as an audit.anchor.s3 entry.
type S3Worker struct {
	db       *gorm.DB
	server   string
	endpoint string
	target   *s3Target
	cancel   context.CancelFunc
	done     sync.WaitGroup
}

// NewS3Worker refuses an endpoint that is not https://. An empty endpoint
// means the AWS default for the region, which is HTTPS. The credentials are
// never written to the audit log.
func NewS3Worker(db *gorm.DB, server string, c config.AuditAnchorS3Config) (*S3Worker, error) {
	// Clone the default transport so an operator CA loaded at startup applies.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{
		Transport:     transport,
		Timeout:       httpsAttemptTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return newS3Worker(db, server, c, client)
}

func newS3Worker(db *gorm.DB, server string, c config.AuditAnchorS3Config, client *http.Client) (*S3Worker, error) {
	endpoint := "s3." + c.Region + ".amazonaws.com"
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return nil, errors.New("audit anchor object storage endpoint must be an https:// URL with a host and no credentials")
		}
		endpoint = u.Host
	}
	if c.Bucket == "" || c.Region == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, errors.New("audit anchor object storage requires bucket, region, access key ID and secret access key")
	}
	s3Client := s3.NewFromConfig(aws.Config{
		Region:      c.Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, "")),
		HTTPClient:  client,
		// Attempts are counted and recorded here, not hidden in SDK retries.
		RetryMaxAttempts: 1,
		// Content-MD5 is set explicitly; skip the SDK's optional streaming
		// checksum, which not every S3 store supports.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
		o.UsePathStyle = c.ForcePathStyle
	})
	return &S3Worker{db: db, server: server, endpoint: endpoint, target: &s3Target{client: s3Client, bucket: c.Bucket, prefix: c.Prefix, backoff: 2 * time.Second}}, nil
}

// Start begins the background loop. Call once, then Shutdown before closing DB.
func (w *S3Worker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			// Room for every attempt and its backoff, plus the database work.
			_ = tracing.Job(ctx, "job.audit_anchor_s3", func(jobCtx context.Context) error {
				checkCtx, stop := context.WithTimeout(jobCtx, 50*time.Second)
				defer stop()
				_, err := w.Check(checkCtx, time.Now())
				if err != nil && ctx.Err() == nil {
					slog.ErrorContext(jobCtx, "audit anchor s3: publication failed", "error", err)
				}
				return err
			})
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Shutdown cancels an in-flight check and waits for its transaction to finish.
func (w *S3Worker) Shutdown() {
	if w.cancel != nil {
		w.cancel()
		w.done.Wait()
	}
}

// Check writes a due anchor and records the attempt, returning true only when
// the object was stored and the audit entry committed. A refused or
// unreachable store is recorded as a failed entry and returned as an error.
func (w *S3Worker) Check(ctx context.Context, now time.Time) (bool, error) {
	if rmail.ContainsControlChar(w.server) || len(w.server) == 0 || len(w.server) > 255 {
		return false, fmt.Errorf("invalid audit anchor server name")
	}
	d, err := publishExtra(ctx, w.db, now, extraTarget{
		action: s3Action, lock: s3PublicationLock, server: w.server,
		fields: models.AuditTarget{"endpoint": w.endpoint, "bucket": w.target.bucket},
		deliver: func(ctx context.Context, now time.Time, head audit.Anchor, body string) delivery {
			return w.target.put(ctx, w.target.key(now, head), body)
		},
	})
	if err != nil {
		return false, fmt.Errorf("record audit anchor s3: %w", err)
	}
	if d == nil {
		return false, nil
	}
	if d.result != "accepted" {
		return false, fmt.Errorf("audit anchor s3 %s after %d attempts: %v", d.result, d.attempts, d.err)
	}
	slog.Info("audit anchor s3 stored", "endpoint", w.endpoint, "bucket", w.target.bucket)
	return true, nil
}

// key names one object per attempt: the zero-padded seq sorts the objects in
// chain order, and the time gives a later attempt at the same seq its own name.
func (t *s3Target) key(now time.Time, head audit.Anchor) string {
	return fmt.Sprintf("%s%020d-%s.txt", t.prefix, head.Seq, now.UTC().Format("20060102T150405.000000Z"))
}
