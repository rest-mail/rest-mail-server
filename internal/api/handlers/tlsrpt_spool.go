package handlers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"mime"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
)

// A report is read from a file, never from memory. Over the cap, compressed or not, it never
// reaches memory at all. Under it, the binder's rules are applied a piece at a time: a field
// the type doesn't declare is passed over, and each policy entry is read on its own.
//
// Two things still hold a whole report, both within tlsRPTMaxBodyBytes: a row keeps it whole
// in raw_report (readWholeReport), and a report the piecewise reading can't speak for is
// bound whole (see decodeSpooledReport).

const tlsReportGzipMedia = "application/tlsrpt+gzip"

// maxJSONNesting is the depth encoding/json's scanner refuses past, which token-by-token
// reading does not enforce.
const maxJSONNesting = 10000

// tlsReportFields are the fields TLSRPTReport declares, and tlsReportMaxPolicies the most
// policy entries it takes. Past that no entry is kept, and counting one more is enough for the
// binder to refuse the count. Both are read from the type, so they can't come apart from it.
var tlsReportFields, tlsReportMaxPolicies = tlsReportShape()

func tlsReportShape() (fields []string, maxPolicies int) {
	t := reflect.TypeOf(TLSRPTReport{})
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if name, _, _ := strings.Cut(field.Tag.Get("json"), ","); name != "" {
			fields = append(fields, name)
		}
		if field.Name == "Policies" {
			maxPolicies, _ = strconv.Atoi(field.Tag.Get("maxItems"))
		}
	}
	return fields, maxPolicies
}

// tlsReportPolicyProbe holds a single policy entry in an otherwise valid report, so that the
// binder judges and decodes the entry in its place, in its words.
const (
	tlsReportPolicyProbeOpen  = `{"organization-name":"x","date-range":{"start-datetime":"2020-01-01T00:00:00Z","end-datetime":"2020-01-01T00:00:00Z"},"policies":[`
	tlsReportPolicyProbeClose = `]}`
	tlsReportPolicyProbePath  = "policies[0]"
)

func invalidTLSReportBody() *endpoint.Error {
	return endpoint.Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
}

// tlsReportReadFailure is the error for a failed read of a spooled report: the server's own
// disk as it failed, anything else the report's fault.
func tlsReportReadFailure(err error) error {
	var disk *fs.PathError
	if errors.As(err, &disk) {
		return err
	}
	return invalidTLSReportBody()
}

// receiveSpooledReport is the route's handler. It holds the request to what binding it would:
// the body goes to a file, its media type and content are checked, a body labelled gzip
// inflates to a file of its own, and the report is decoded from there. Every file is gone
// when it answers, however it answers. A body that came compressed in another way arrives here
// already inflating (inflateReport), so it spools inflated.
func (h *TLSReportHandler) receiveSpooledReport(r *http.Request, _ struct{}) (tlsReportStored, error) {
	file, done, err := endpoint.SpoolBody(r, "")
	if err != nil {
		return tlsReportStored{}, err
	}
	defer done()
	if err := endpoint.CheckSpooled(r, &receiveTLSReportInput{}, file); err != nil {
		return tlsReportStored{}, err
	}

	// Bound to the JSON form, null is a report left nil. Handed to the binder after
	// inflating, it is refused.
	nullAllowed := true
	if media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); media == tlsReportGzipMedia {
		inflated, doneInflating, err := spoolInflatedReport(file)
		if err != nil {
			return tlsReportStored{}, err
		}
		defer doneInflating()
		file, nullAllowed = inflated, false
	}

	report, err := decodeSpooledReport(file, nullAllowed)
	if err != nil {
		return tlsReportStored{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return tlsReportStored{}, err
	}
	r.Body = io.NopCloser(file)
	return h.receiveReport(r, receiveTLSReportInput{Report: report})
}

// spoolInflatedReport inflates a compressed report to a file of its own, held to the same cap
// as one sent plain, so a small compressed body cannot expand without bound: no more than a
// byte past the cap is ever written. The answer is the file positioned at its start and a
// function that removes it. It refuses with 413 past the cap, 400 for a stream that is not gzip.
func spoolInflatedReport(compressed io.Reader) (*os.File, func(), error) {
	notGzip := func(err error) error {
		var disk *fs.PathError
		if errors.As(err, &disk) {
			return err
		}
		return endpoint.Refuse(http.StatusBadRequest, "bad_request", "Invalid gzip-compressed TLS-RPT report")
	}
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, nil, notGzip(err)
	}
	defer func() { _ = reader.Close() }()
	file, done, err := endpoint.Spool(io.LimitReader(reader, tlsRPTMaxBodyBytes+1))
	if err != nil {
		return nil, nil, notGzip(err)
	}
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		done()
		return nil, nil, err
	}
	if size > tlsRPTMaxBodyBytes {
		done()
		return nil, nil, endpoint.Refuse(http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		done()
		return nil, nil, err
	}
	return file, done, nil
}

// readWholeReport reads a report as it was sent, for raw_report, which keeps it whole. A
// spooled report is already within the cap; the limit keeps this read to it regardless.
func readWholeReport(src io.Reader) ([]byte, error) {
	whole, err := io.ReadAll(io.LimitReader(src, tlsRPTMaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(whole)) > tlsRPTMaxBodyBytes {
		return nil, errors.New("report exceeds body limit")
	}
	return whole, nil
}

// decodeSpooledReport reads a report from a spooled file with a streaming decoder, and answers
// as endpoint.BindBody would for the same bytes: the same report, or the same refusal.
//
// The rules stay the binder's, as importContacts leaves them. Each piece goes through it in a
// report otherwise valid, and the fields it refuses are added up. The whole report is bound
// instead, read back within the cap, when a piece can't speak for it: a declared field named
// twice, or in another case, which encoding/json merges; a policies that isn't an array; or a
// value the rules pass and decoding refuses, whose words name its place in the whole. Any of
// those is unusual, and most end in a refusal.
//
// null is refused too, unless nullAllowed, when it answers a nil report and no error.
func decodeSpooledReport(file *os.File, nullAllowed bool) (*TLSRPTReport, error) {
	valid, err := spooledJSONValid(file)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, invalidTLSReportBody()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	root, err := decoder.Token()
	if err != nil {
		return nil, tlsReportReadFailure(err)
	}
	switch root {
	case json.Delim('{'):
	case nil:
		if nullAllowed {
			return nil, nil
		}
		return nil, endpoint.BindBody(&TLSRPTReport{}, []byte("null"))
	default:
		// Only the first byte decides this refusal, so a stand-in gets its words.
		return nil, endpoint.BindBody(&TLSRPTReport{}, []byte("[]"))
	}

	pieces, whole, err := readReportPieces(decoder)
	if err != nil {
		return nil, tlsReportReadFailure(err)
	}
	if whole {
		return bindWholeSpooledReport(file)
	}
	report, whole, err := pieces.bind()
	if err != nil {
		return nil, err
	}
	if whole {
		return bindWholeSpooledReport(file)
	}
	return report, nil
}

// bindWholeSpooledReport is the binder on the whole report, for what its pieces can't answer.
func bindWholeSpooledReport(file *os.File) (*TLSRPTReport, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	body, err := readWholeReport(file)
	if err != nil {
		return nil, err
	}
	report := &TLSRPTReport{}
	if err := endpoint.BindBody(report, body); err != nil {
		return nil, err
	}
	return report, nil
}

// spooledJSONValid says what json.Valid would about a spooled file, a token at a time.
func spooledJSONValid(file *os.File) (bool, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	decoder := json.NewDecoder(file)
	decoder.UseNumber() // A number too large for a float64 is still valid JSON.
	if err := skipJSONValue(decoder); err != nil {
		var disk *fs.PathError
		if errors.As(err, &disk) {
			return false, err
		}
		return false, nil
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		var disk *fs.PathError
		if errors.As(err, &disk) {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

// skipJSONValue reads the next value a token at a time, keeping none of it.
func skipJSONValue(decoder *json.Decoder) error {
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'), json.Delim('['):
			depth++
			if depth > maxJSONNesting {
				return errors.New("exceeded max depth")
			}
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}

// tlsReportPieces is a report as its top level was read.
type tlsReportPieces struct {
	fields      map[string]json.RawMessage
	policies    []TLSRPTPolicy
	problems    map[string]string
	policyCount int
	hasPolicies bool
	// undecodable is an entry the rules pass and decoding refuses.
	undecodable bool
}

// readReportPieces reads the top level of a report whose opening brace has been read, and
// says whether the whole report has to be bound instead.
func readReportPieces(decoder *json.Decoder) (*tlsReportPieces, bool, error) {
	pieces := &tlsReportPieces{fields: map[string]json.RawMessage{}, problems: map[string]string{}}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, false, err
		}
		key, _ := token.(string)
		declared := tlsReportFieldNamed(key)
		_, seen := pieces.fields[key]
		seen = seen || (key == "policies" && pieces.hasPolicies)
		switch {
		case declared == "":
			if err := skipJSONValue(decoder); err != nil {
				return nil, false, err
			}
		case declared != key || seen:
			return nil, true, nil
		case key == "policies":
			pieces.hasPolicies = true
			open, err := decoder.Token()
			if err != nil {
				return nil, false, err
			}
			if open != json.Delim('[') {
				return nil, true, nil
			}
			if err := pieces.readPolicies(decoder); err != nil {
				return nil, false, err
			}
		default:
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return nil, false, err
			}
			pieces.fields[key] = raw
		}
	}
	return pieces, pieces.undecodable, nil
}

// tlsReportFieldNamed is the declared field encoding/json would read key into, whatever its
// case: encoding/json matches a name as strings.EqualFold does.
func tlsReportFieldNamed(key string) string {
	for _, name := range tlsReportFields {
		if strings.EqualFold(key, name) {
			return name
		}
	}
	return ""
}

// readPolicies reads a policies array whose opening bracket has been read, through its close.
func (p *tlsReportPieces) readPolicies(decoder *json.Decoder) error {
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		probe := make([]byte, 0, len(tlsReportPolicyProbeOpen)+len(raw)+len(tlsReportPolicyProbeClose))
		probe = append(append(append(probe, tlsReportPolicyProbeOpen...), raw...), tlsReportPolicyProbeClose...)
		var holder TLSRPTReport
		err := endpoint.BindBody(&holder, probe)
		switch refusal := (*endpoint.Error)(nil); {
		case err == nil:
			if len(p.problems) == 0 && p.policyCount < tlsReportMaxPolicies {
				p.policies = append(p.policies, holder.Policies...)
			}
		case errors.As(err, &refusal):
			p.addPolicyProblems(refusal.Fields)
		default:
			return err
		}
		p.policyCount++
	}
	_, err := decoder.Token()
	return err
}

// addPolicyProblems records what the binder said of the entry being read, at its real index.
// What it says of anything but the entry is decoding's complaint, not a rule's.
func (p *tlsReportPieces) addPolicyProblems(fields map[string]string) {
	for field, problem := range fields {
		rest, ok := strings.CutPrefix(field, tlsReportPolicyProbePath)
		if !ok {
			p.undecodable = true
			continue
		}
		p.problems[fmt.Sprintf("policies[%d]%s", p.policyCount, rest)] = problem
	}
}

// bind holds the top level to the binder with each policy entry stood in for by an empty one,
// which the rules pass, so their number is still judged, and answers the report or everything
// wrong with it. It says when the whole report has to be bound instead.
func (p *tlsReportPieces) bind() (*TLSRPTReport, bool, error) {
	var top bytes.Buffer
	top.WriteByte('{')
	for key, raw := range p.fields {
		if top.Len() > 1 {
			top.WriteByte(',')
		}
		top.WriteString(`"` + key + `":`)
		top.Write(raw)
	}
	if p.hasPolicies {
		if top.Len() > 1 {
			top.WriteByte(',')
		}
		top.WriteString(`"policies":[`)
		for i := 0; i < min(p.policyCount, tlsReportMaxPolicies+1); i++ {
			if i > 0 {
				top.WriteByte(',')
			}
			top.WriteString(`{}`)
		}
		top.WriteByte(']')
	}
	top.WriteByte('}')

	report := &TLSRPTReport{}
	err := endpoint.BindBody(report, top.Bytes())
	var refusal *endpoint.Error
	switch {
	case err == nil:
	case errors.As(err, &refusal):
		if _, undecodable := refusal.Fields["body"]; undecodable {
			return nil, true, nil
		}
		maps.Copy(p.problems, refusal.Fields)
	default:
		return nil, false, err
	}
	if len(p.problems) > 0 {
		return nil, false, endpoint.Invalid(p.problems)
	}
	report.Policies = p.policies
	return report, false, nil
}
