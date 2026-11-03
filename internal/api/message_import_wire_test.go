package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	importWireDate    = `"internal_date":"2020-01-02T03:04:05Z"`
	importWireMessage = "Message-ID: <wire@example.test>\r\nFrom: Old <old@example.test>\r\nSubject: s\r\n\r\nbody\r\n"
)

var (
	importWireValid    = importWireBase64(importWireMessage)
	importWireOversize = importWireBase64(strings.Repeat("x", 1025))
	importWireAtLimit  = importWireBase64("Subject: s\r\n\r\n" + strings.Repeat("x", 1024-14))
	importWireNumbers  = func() string {
		numbers, _ := json.Marshal(func() []int {
			list := make([]int, len(importWireMessage))
			for i := range list {
				list[i] = int(importWireMessage[i])
			}
			return list
		}())
		return string(numbers)
	}()
)

func importWireBase64(message string) string {
	return base64.StdEncoding.EncodeToString([]byte(message))
}

// importWireCases are requests and what the route answers them, byte for byte. Each of these
// answers was recorded from the route as it stood when it bound the whole request in memory, so
// this table holds the route to them however it reads a request.
var importWireCases = []struct {
	name, path, contentType, body string
	status                        int
	answer                        string
}{
	{name: `valid`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `valid_with_folder_and_flags`, body: `{"raw_message":"` + importWireValid + `","folder":"Archive","flags":["\\Seen","$Junk","\\Deleted"],` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"Archive"}}`},
	{name: `valid_inbox_any_case`, body: `{"raw_message":"` + importWireValid + `","folder":"inbox",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `valid_field_order`, body: `{` + importWireDate + `,"folder":"A","raw_message":"` + importWireValid + `"}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"A"}}`},
	{name: `valid_newlines_in_base64`, body: "{\"raw_message\":\"TWVzc2FnZS1JRDogPHdpcmVAZXhhbXBsZS50ZXN0\\r\\nPg0KRnJvbTogT2xkIDxvbGRAZXhhbXBsZS50ZXN0Pg0KU3ViamVjdDogcw0KDQpib2R5DQo=\"," + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `valid_escaped_base64`, body: `{"raw_message":"\u0054WVzc2FnZS1JRDogPHdpcmVAZXhhbXBsZS50ZXN0Pg0KRnJvbTogT2xkIDxvbGRAZXhhbXBsZS50ZXN0Pg0KU3ViamVjdDogcw0KDQpib2R5DQo=",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `valid_no_content_type`, contentType: `-`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `valid_content_type_parameter`, contentType: `application/json; charset=utf-8`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `valid_upper_content_type`, contentType: `APPLICATION/JSON`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `valid_array_of_numbers`, body: `{"raw_message":` + importWireNumbers + `,` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `no_message_id`, body: `{"raw_message":"RnJvbTogYUBleGFtcGxlLnRlc3QNCg0KYm9keQ0K",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cimport.70365fab512d6c19b32abd1e05692b5f0c2424789bddb7148f86acee9f89f46b@rest-mail.invalid\u003e","folder":"INBOX"}}`},
	{name: `not_utf8`, body: `{"raw_message":"TWVzc2FnZS1JRDogPG5AZXhhbXBsZS50ZXN0Pg0KU3ViamVjdDog6Q0KDQpib2Tp/w0K",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cn@example.test\u003e","folder":"INBOX"}}`},
	{name: `nul`, body: `{"raw_message":"TWVzc2FnZS1JRDogPHpAZXhhbXBsZS50ZXN0Pg0KDQpibwBkeQ0K",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cz@example.test\u003e","folder":"INBOX"}}`},
	{name: `nul_in_message_id`, body: `{"raw_message":"TWVzc2FnZS1JRDogPHoAQGV4YW1wbGUudGVzdD4NCg0KYm9keQ0K",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cimport.4ff7568c40a5b0edec06808ee069a12fad50eec937670964be021b769a4ae941@rest-mail.invalid\u003e","folder":"INBOX"}}`},
	{name: `long_message_id`, body: `{"raw_message":"` + importWireBase64("Message-ID: <"+strings.Repeat("x", 996)+">\r\n\r\nbody\r\n") + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"Message-ID exceeds 995 bytes"}}}}`},
	{name: `not_a_message`, body: `{"raw_message":"bm8gY29sb24gaGVyZQ0KDQpib2R5",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a parseable RFC 5322 message"}}}}`},
	{name: `duplicate_import`, body: `{"raw_message":"` + importWireValid + `","folder":"Archive",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":false,"message_id":"\u003cwire@example.test\u003e","folder":"Archive"}}`},
	{name: `invalid_base64`, body: `{"raw_message":"!!!!",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `invalid_base64_late`, body: `{"raw_message":"` + importWireValid + `!",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `invalid_base64_unpadded`, body: `{"raw_message":"QQ",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `invalid_base64_data_after_padding`, body: `{"raw_message":"QQ==QQ==",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `invalid_base64_escape`, body: `{"raw_message":"\u00e9QQ=",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `invalid_base64_not_utf8`, body: "{\"raw_message\":\"\xff\"," + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `invalid_base64_and_oversize`, body: `{"raw_message":"` + strings.Repeat(`A`, 2000) + `!",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `invalid_base64_and_bad_flags`, body: `{"raw_message":"!!!!","flags":1,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"flags":"must be an array","raw_message":"must be a base64 string"}}}}`},
	{name: `oversize_decoded`, body: `{"raw_message":"` + importWireOversize + `",` + importWireDate + `}`,
		status: 413, answer: `{"error":{"code":"message_too_large","message":"The decoded message exceeds the import size limit"}}`},
	{name: `limit_exactly`, body: `{"raw_message":"` + importWireAtLimit + `",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cimport.aeb1dd4b247fab4d2fc916559253f95fa74f90ac042d29c3964ab2f1b6abdf44@rest-mail.invalid\u003e","folder":"INBOX"}}`},
	{name: `oversize_and_bad_folder`, body: `{"raw_message":"` + importWireOversize + `","folder":"bad\u0000folder",` + importWireDate + `}`,
		status: 413, answer: `{"error":{"code":"message_too_large","message":"The decoded message exceeds the import size limit"}}`},
	{name: `oversize_and_bad_date`, body: `{"raw_message":"` + importWireOversize + `","internal_date":"yesterday"}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"internal_date":"must be a date-time"}}}}`},
	{name: `oversize_and_unknown_field`, body: `{"raw_message":"` + importWireOversize + `","x":1,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"x":"not allowed"}}}}`},
	{name: `oversize_and_bad_account`, path: `0`, body: `{"raw_message":"` + importWireOversize + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Path parameters failed validation","details":{"fields":{"id":"must be at least 1"}}}}`},
	{name: `over_request_cap`, body: `{"raw_message":"` + strings.Repeat(`A`, 70000) + `"}`,
		status: 413, answer: `{"error":{"code":"request_too_large","message":"Request body too large"}}`},
	{name: `over_request_cap_not_json`, body: strings.Repeat(`x`, 70001),
		status: 413, answer: `{"error":{"code":"request_too_large","message":"Request body too large"}}`},
	{name: `over_request_cap_wrong_media`, contentType: `text/plain`, body: strings.Repeat(`x`, 70001),
		status: 413, answer: `{"error":{"code":"request_too_large","message":"Request body too large"}}`},
	{name: `empty`, body: ``,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `empty_wrong_media`, contentType: `text/plain`, body: ``,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `empty_no_media`, contentType: `-`, body: ``,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `whitespace`, body: " \r\n\t",
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `whitespace_wrong_media`, contentType: `text/plain`, body: `  `,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Unsupported media type: text/plain; the body is taken as application/json"}}`},
	{name: `empty_message`, body: `{"raw_message":"",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"required: a complete RFC 5322 message"}}}}`},
	{name: `empty_message_newline`, body: "{\"raw_message\":\"\\n\"," + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"required: a complete RFC 5322 message"}}}}`},
	{name: `empty_message_and_bad_folder`, body: `{"raw_message":"","folder":"bad\u0000folder",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"folder":"must not contain a control character","raw_message":"required: a complete RFC 5322 message"}}}}`},
	{name: `empty_message_no_date`, body: `{"raw_message":""}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"internal_date":"required"}}}}`},
	{name: `missing_message`, body: `{` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"required"}}}}`},
	{name: `empty_object`, body: `{}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"internal_date":"required","raw_message":"required"}}}}`},
	{name: `null_message`, body: `{"raw_message":null,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must not be null"}}}}`},
	{name: `number_message`, body: `{"raw_message":1,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `object_message`, body: `{"raw_message":{},` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `bad_array_message`, body: `{"raw_message":[300],` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `empty_array_message`, body: `{"raw_message":[],` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"required: a complete RFC 5322 message"}}}}`},
	{name: `malformed_json`, body: `{`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `malformed_json_after_message`, body: `{"raw_message":"` + importWireValid + `",`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `malformed_json_trailing`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `} x`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `malformed_json_trailing_comma`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `,}`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `malformed_json_bad_string`, body: "{\"raw_message\":\"QQ==\n\"," + importWireDate + `}`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `malformed_json_bad_escape`, body: "{\"raw_message\":\"QQ\\x==\"," + importWireDate + `}`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `malformed_json_bad_account`, path: `0`, body: `{`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `malformed_json_wrong_media`, contentType: `text/plain`, body: `{`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Unsupported media type: text/plain; the body is taken as application/json"}}`},
	{name: `bad_media_type_valid_body`, contentType: `text/plain`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Unsupported media type: text/plain; the body is taken as application/json"}}`},
	{name: `unparseable_media_type`, contentType: `a b/c; =`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Unsupported media type: a b/c; =; the body is taken as application/json"}}`},
	{name: `root_array`, body: `[]`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is not a JSON object"}}}}`},
	{name: `root_big_array`, body: `[1,2,3,{"raw_message":"QQ=="}]`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is not a JSON object"}}}}`},
	{name: `root_null`, body: `null`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is null"}}}}`},
	{name: `root_null_spaced`, body: " null\n",
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is null"}}}}`},
	{name: `root_string`, body: `"x"`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is not a JSON object"}}}}`},
	{name: `root_number`, body: `12`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is not a JSON object"}}}}`},
	{name: `root_true`, body: `true`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is not a JSON object"}}}}`},
	{name: `root_array_bad_account`, path: `0`, body: `[]`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"the body is not a JSON object"}}}}`},
	{name: `duplicate_message_last_wins`, body: `{"raw_message":"!!!!","raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"illegal base64 data at input byte 0"}}}}`},
	{name: `duplicate_message_valid_then_valid`, body: `{"raw_message":"QQ==","raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `duplicate_message_valid_then_invalid`, body: `{"raw_message":"` + importWireValid + `","raw_message":"!!!!",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must be a base64 string"}}}}`},
	{name: `duplicate_message_invalid_late_then_valid`, body: `{"raw_message":"QUJD!","raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"illegal base64 data at input byte 4"}}}}`},
	{name: `duplicate_message_number_then_valid`, body: `{"raw_message":1,"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"json: cannot unmarshal number into Go struct field ImportMessageBody.raw_message of type []uint8"}}}}`},
	{name: `duplicate_message_null_then_valid`, body: `{"raw_message":null,"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `duplicate_message_valid_then_null`, body: `{"raw_message":"` + importWireValid + `","raw_message":null,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"must not be null"}}}}`},
	{name: `duplicate_message_valid_then_empty`, body: `{"raw_message":"` + importWireValid + `","raw_message":"",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"raw_message":"required: a complete RFC 5322 message"}}}}`},
	{name: `duplicate_message_oversize_then_valid`, body: `{"raw_message":"` + importWireOversize + `","raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `duplicate_message_valid_then_oversize`, body: `{"raw_message":"` + importWireValid + `","raw_message":"` + importWireOversize + `",` + importWireDate + `}`,
		status: 413, answer: `{"error":{"code":"message_too_large","message":"The decoded message exceeds the import size limit"}}`},
	{name: `duplicate_message_by_escape`, body: `{"raw\u005fmessage":"!!!!","raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"illegal base64 data at input byte 0"}}}}`},
	{name: `duplicate_folder`, body: `{"raw_message":"` + importWireValid + `","folder":"A","folder":"B",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"B"}}`},
	{name: `duplicate_folder_number_then_valid`, body: `{"raw_message":"` + importWireValid + `","folder":1,"folder":"B",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"json: cannot unmarshal number into Go struct field ImportMessageBody.folder of type string"}}}}`},
	{name: `duplicate_flags`, body: `{"raw_message":"` + importWireValid + `","flags":["\\Seen"],"flags":["$Junk"],` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `duplicate_date`, body: `{"raw_message":"` + importWireValid + `","internal_date":"yesterday",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":"parsing time \"yesterday\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \"yesterday\" as \"2006\""}}}}`},
	{name: `escaped_message_key`, body: `{"raw\u005fmessage":"` + importWireValid + `",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `escaped_folder_key`, body: `{"raw_message":"` + importWireValid + `","fol\u0064er":"Esc",` + importWireDate + `}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"Esc"}}`},
	{name: `upper_case_key`, body: `{"RAW_MESSAGE":"` + importWireValid + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"RAW_MESSAGE":"not allowed","raw_message":"required"}}}}`},
	{name: `both_case_keys`, body: `{"raw_message":"` + importWireValid + `","Raw_Message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"Raw_Message":"not allowed"}}}}`},
	{name: `unknown_field`, body: `{"raw_message":"` + importWireValid + `","x":{"deep":[1,2]},` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"x":"not allowed"}}}}`},
	{name: `two_unknown_fields`, body: `{"raw_message":"` + importWireValid + `","x":1,"y":2,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"x":"not allowed","y":"not allowed"}}}}`},
	{name: `unknown_not_utf8_key`, body: `{"raw_message":"` + importWireValid + "\",\"\xff\":1," + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"�":"not allowed"}}}}`},
	{name: `unknown_and_bad_account`, path: `0`, body: `{"raw_message":"` + importWireValid + `","x":1,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request failed validation","details":{"fields":{"id":"must be at least 1","x":"not allowed"}}}}`},
	{name: `bad_account`, path: `0`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Path parameters failed validation","details":{"fields":{"id":"must be at least 1"}}}}`},
	{name: `bad_account_word`, path: `abc`, body: `{"raw_message":"` + importWireValid + `",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Path parameters failed validation","details":{"fields":{"id":"must be a non-negative integer"}}}}`},
	{name: `bad_account_and_bad_base64`, path: `0`, body: `{"raw_message":"!!!!",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request failed validation","details":{"fields":{"id":"must be at least 1","raw_message":"must be a base64 string"}}}}`},
	{name: `bad_account_no_body`, path: `0`, body: ``,
		status: 400, answer: `{"error":{"code":"bad_request","message":"Invalid request body"}}`},
	{name: `bad_folder_type`, body: `{"raw_message":"` + importWireValid + `","folder":1,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"folder":"must be a string"}}}}`},
	{name: `null_folder`, body: `{"raw_message":"` + importWireValid + `","folder":null,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"folder":"must not be null"}}}}`},
	{name: `bad_folder_name`, body: `{"raw_message":"` + importWireValid + `","folder":"bad\u0000folder",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"folder":"must not contain a control character"}}}}`},
	{name: `bad_flags_type`, body: `{"raw_message":"` + importWireValid + `","flags":"x",` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"flags":"must be an array"}}}}`},
	{name: `null_flags`, body: `{"raw_message":"` + importWireValid + `","flags":null,` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"flags":"must not be null"}}}}`},
	{name: `bad_flag`, body: `{"raw_message":"` + importWireValid + `","flags":["bad keyword"],` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"flags":"invalid keyword: expected an RFC 3501 atom without backslash"}}}}`},
	{name: `bad_flag_and_folder`, body: `{"raw_message":"` + importWireValid + `","folder":"bad\u0000folder","flags":["bad keyword"],` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"flags":"invalid keyword: expected an RFC 3501 atom without backslash","folder":"must not contain a control character"}}}}`},
	{name: `system_flag_unknown`, body: `{"raw_message":"` + importWireValid + `","flags":["\\Recent"],` + importWireDate + `}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"flags":"invalid keyword: expected an RFC 3501 atom without backslash"}}}}`},
	{name: `missing_date`, body: `{"raw_message":"` + importWireValid + `"}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"internal_date":"required"}}}}`},
	{name: `bad_date`, body: `{"raw_message":"` + importWireValid + `","internal_date":"yesterday"}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"internal_date":"must be a date-time"}}}}`},
	{name: `null_date`, body: `{"raw_message":"` + importWireValid + `","internal_date":null}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"internal_date":"must not be null"}}}}`},
	{name: `zero_date`, body: `{"raw_message":"` + importWireValid + `","internal_date":"0001-01-01T00:00:00Z"}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"internal_date":"required: the original internal date as an RFC 3339 timestamp"}}}}`},
	{name: `date_with_zone`, body: `{"raw_message":"` + importWireValid + `","internal_date":"2020-01-02T03:04:05+02:00"}`,
		status: 200, answer: `{"data":{"id":41,"imported":true,"message_id":"\u003cwire@example.test\u003e","folder":"INBOX"}}`},
	{name: `everything_wrong`, path: `0`, body: `{"raw_message":"!!!!","folder":1,"flags":1,"internal_date":1,"x":1}`,
		status: 422, answer: `{"error":{"code":"validation_failed","message":"Request failed validation","details":{"fields":{"flags":"must be an array","folder":"must be a string","id":"must be at least 1","internal_date":"must be a date-time","raw_message":"must be a base64 string","x":"not allowed"}}}}`},
}

func TestMessageImportWireAnswers(t *testing.T) {
	useRealGrants(t)
	for _, tc := range importWireCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t, "")
			path := tc.path
			if path == "" {
				path = "12"
			}
			send := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/"+path+"/messages/import", strings.NewReader(tc.body))
				if tc.contentType != "-" {
					contentType := tc.contentType
					if contentType == "" {
						contentType = "application/json"
					}
					r.Header.Set("Content-Type", contentType)
				}
				r.Header.Set("Authorization", "Bearer "+f.token)
				w := httptest.NewRecorder()
				f.router.ServeHTTP(w, r)
				return w
			}
			w := send()
			if tc.name == "duplicate_import" {
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"imported":true`) {
					t.Fatalf("first import: %d %s", w.Code, w.Body)
				}
				w = send()
			}
			if w.Code != tc.status || strings.TrimSpace(w.Body.String()) != tc.answer {
				t.Fatalf("answered %d %s\nwant     %d %s", w.Code, w.Body, tc.status, tc.answer)
			}
		})
	}
}

// The route's own stored bytes for the kinds of message a text column can't hold.
func TestMessageImportWireStoredSource(t *testing.T) {
	useRealGrants(t)
	for _, tc := range []struct {
		name, message string
		text          bool
	}{
		{"text", "Message-ID: <t@example.test>\r\nSubject: s\r\n\r\nbody\r\n", true},
		{"utf8", "Message-ID: <u@example.test>\r\nSubject: s\r\n\r\nbodé\r\n", true},
		{"not utf8", "Message-ID: <n@example.test>\r\nSubject: s\r\n\r\nbod\xe9\r\n", false},
		{"nul", "Message-ID: <z@example.test>\r\nSubject: s\r\n\r\nbo\x00dy\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t, "")
			body := `{"raw_message":"` + importWireBase64(tc.message) + `",` + importWireDate + `}`
			r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/12/messages/import", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+f.token)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, r)
			if w.Code != 200 || len(f.c.state.messages) != 1 {
				t.Fatalf("answered %d %s", w.Code, w.Body)
			}
			stored := f.c.state.messages[0]
			if tc.text {
				octets, _ := stored["raw_octets"].([]byte)
				if stored["raw_message"] != tc.message || len(octets) != 0 {
					t.Fatalf("stored %q %q", stored["raw_message"], stored["raw_octets"])
				}
			} else {
				octets, _ := stored["raw_octets"].([]byte)
				if stored["raw_message"] != "" || !bytes.Equal(octets, []byte(tc.message)) {
					t.Fatalf("stored %q %q", stored["raw_message"], stored["raw_octets"])
				}
			}
			if stored["raw_size"] != int64(len(tc.message)) {
				t.Fatalf("size %v, want %d", stored["raw_size"], len(tc.message))
			}
		})
	}
}
