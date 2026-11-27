package dmarcreceive

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/mail"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

const (
	MaxBytes      = 512 << 10
	MaxRows       = 100
	MaxZipEntries = 10
)

type feedback struct {
	Metadata struct {
		OrgName  string `xml:"org_name"`
		Email    string `xml:"email"`
		ReportID string `xml:"report_id"`
		Period   *struct {
			Begin *int64 `xml:"begin"`
			End   *int64 `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	Policy  models.DMARCReceivedPolicy   `xml:"policy_published"`
	Records []models.DMARCReceivedRecord `xml:"record"`
}

// Parse reads one DMARC aggregate report attachment. A compressed report is inflated to a
// temporary file rather than into memory, and each document is checked and decoded from that
// file with streaming XML decoders. The files are removed before Parse returns, whatever the
// outcome. The byte, entry and row limits are the same for every media type.
func Parse(media string, data []byte) ([]models.DMARCReportReceived, error) {
	if len(data) > MaxBytes {
		return nil, errors.New("compressed report exceeds size limit")
	}
	var documents []io.ReadSeeker
	var spools []*os.File
	defer func() {
		for _, file := range spools {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	inflate := func(src io.Reader, limit int) (int, error) {
		file, err := os.CreateTemp("", "restmail-dmarc-report-*")
		if err != nil {
			return 0, errors.New("report spool unavailable")
		}
		spools = append(spools, file)
		if spoolCreated != nil {
			spoolCreated(file.Name())
		}
		size, err := spoolBounded(file, src, limit)
		if err != nil {
			return 0, err
		}
		documents = append(documents, file)
		return size, nil
	}
	switch media {
	case "text/xml":
		documents = append(documents, bytes.NewReader(data))
	case "application/gzip":
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, errors.New("invalid gzip report")
		}
		_, err = inflate(reader, MaxBytes)
		_ = reader.Close()
		if err != nil {
			return nil, err
		}
	case "application/zip":
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, errors.New("invalid zip report")
		}
		if len(reader.File) == 0 || len(reader.File) > MaxZipEntries {
			return nil, errors.New("zip entry limit")
		}
		remaining := MaxBytes
		for _, entry := range reader.File {
			if entry.FileInfo().IsDir() {
				continue
			}
			if entry.UncompressedSize64 > uint64(remaining) {
				return nil, errors.New("inflated zip exceeds size limit")
			}
			stream, err := entry.Open()
			if err != nil {
				return nil, errors.New("invalid zip entry")
			}
			size, err := inflate(stream, remaining)
			_ = stream.Close()
			if err != nil {
				return nil, err
			}
			remaining -= size
		}
	default:
		return nil, errors.New("unsupported report media type")
	}
	if len(documents) == 0 {
		return nil, errors.New("empty report")
	}
	reports := make([]models.DMARCReportReceived, 0, len(documents))
	rows := 0
	for _, document := range documents {
		report, err := parseXML(document)
		if err != nil {
			return nil, err
		}
		rows += len(report.Records)
		if rows > MaxRows {
			return nil, errors.New("report row limit")
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// spoolCreated, when set by a test, is told the path of each spool file Parse creates.
var spoolCreated func(path string)

// spoolBounded copies at most limit inflated bytes from src into file and rewinds it. One byte
// past the limit is read to tell an exact-limit report from an oversized one; the oversized one
// is refused.
func spoolBounded(file *os.File, src io.Reader, limit int) (int, error) {
	written, err := io.Copy(file, io.LimitReader(src, int64(limit)+1))
	if err != nil {
		var disk *fs.PathError
		if errors.As(err, &disk) {
			return 0, errors.New("report spool write failed")
		}
		return 0, errors.New("invalid compressed report")
	}
	if written > int64(limit) {
		return 0, errors.New("inflated report exceeds size limit")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, errors.New("report spool unavailable")
	}
	return int(written), nil
}

func parseXML(document io.ReadSeeker) (models.DMARCReportReceived, error) {
	var empty models.DMARCReportReceived
	decoder := xml.NewDecoder(document)
	depth, roots, rows := 0, 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return empty, errors.New("invalid report XML")
		}
		switch element := token.(type) {
		case xml.Directive:
			return empty, errors.New("XML directives are forbidden")
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || element.Name.Local != "feedback" || element.Name.Space != "" && element.Name.Space != "urn:ietf:params:xml:ns:dmarc-2.0" {
					return empty, errors.New("not a supported feedback document")
				}
			}
			depth++
			if depth > 32 {
				return empty, errors.New("XML nesting limit")
			}
			if depth == 2 && element.Name.Local == "record" {
				rows++
				if rows > MaxRows {
					return empty, errors.New("report row limit")
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(element)) != 0 {
				return empty, errors.New("data outside feedback document")
			}
		}
	}
	if roots != 1 || depth != 0 {
		return empty, errors.New("incomplete feedback document")
	}
	if _, err := document.Seek(0, io.SeekStart); err != nil {
		return empty, errors.New("report spool unavailable")
	}
	var report feedback
	if err := xml.NewDecoder(document).Decode(&report); err != nil {
		return empty, errors.New("invalid feedback fields")
	}
	if err := boundedFields(reflect.ValueOf(report)); err != nil {
		return empty, err
	}
	metadata := report.Metadata
	if metadata.OrgName == "" || len(metadata.OrgName) > 255 || metadata.ReportID == "" || len(metadata.ReportID) > 255 {
		return empty, errors.New("invalid report metadata")
	}
	if _, err := mail.ParseAddress(metadata.Email); err != nil {
		return empty, errors.New("invalid reporter contact")
	}
	if metadata.Period == nil || metadata.Period.Begin == nil || metadata.Period.End == nil {
		return empty, errors.New("missing report period")
	}
	begin, end := *metadata.Period.Begin, *metadata.Period.End
	if begin < 0 || end < begin || end > 253402300799 {
		return empty, errors.New("invalid report period")
	}
	policy := &report.Policy
	policyDomain, err := rmail.DomainASCII(strings.TrimSuffix(policy.Domain, "."))
	if err != nil {
		return empty, errors.New("invalid policy domain")
	}
	policy.Domain = strings.ToLower(policyDomain)
	if !pipeline.IsHostName(policy.Domain) || len(policy.Domain) > 255 || !oneOf(policy.P, "none", "quarantine", "reject") || !oneOf(policy.SP, "", "none", "quarantine", "reject") || !oneOf(policy.NP, "", "none", "quarantine", "reject") || !oneOf(policy.ADKIM, "", "r", "s") || !oneOf(policy.ASPF, "", "r", "s") || !oneOf(policy.Testing, "", "y", "n") || policy.Pct != nil && *policy.Pct > 100 {
		return empty, errors.New("invalid published policy")
	}
	if len(report.Records) == 0 {
		return empty, errors.New("report has no rows")
	}
	for _, record := range report.Records {
		if _, err := netip.ParseAddr(record.Row.SourceIP); err != nil {
			return empty, errors.New("invalid source IP")
		}
		if record.Row.Count <= 0 || record.Row.Count > 9007199254740991 {
			return empty, errors.New("invalid message count")
		}
		result := record.Row.PolicyEvaluated
		if !oneOf(result.Disposition, "none", "pass", "quarantine", "reject") || !oneOf(result.DKIM, "pass", "fail") || !oneOf(result.SPF, "pass", "fail") || !pipeline.IsHostName(record.Identifiers.HeaderFrom) {
			return empty, errors.New("invalid row results")
		}
	}
	return models.DMARCReportReceived{ReportingOrg: metadata.OrgName, ReportID: metadata.ReportID, StartDate: time.Unix(begin, 0).UTC(), EndDate: time.Unix(end, 0).UTC(), PolicyPublished: report.Policy, Records: report.Records}, nil
}

func boundedFields(value reflect.Value) error {
	switch value.Kind() {
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := value.Field(index)
			tag := value.Type().Field(index).Tag.Get("maxLength")
			if tag != "" && field.Kind() == reflect.String {
				limit, _ := strconv.Atoi(tag)
				if len(field.String()) > limit {
					return fmt.Errorf("report field exceeds %d bytes", limit)
				}
			}
			if err := boundedFields(field); err != nil {
				return err
			}
		}
	case reflect.String:
		if value.Len() > 1024 {
			return errors.New("report field limit")
		}
	case reflect.Slice:
		if value.Len() > MaxRows {
			return errors.New("report collection limit")
		}
		if value.Type().Elem() != reflect.TypeFor[models.DMARCReceivedRecord]() && value.Len() > 10 {
			return errors.New("report result limit")
		}
		for index := 0; index < value.Len(); index++ {
			if err := boundedFields(value.Index(index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
