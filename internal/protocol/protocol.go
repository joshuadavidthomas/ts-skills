// Package protocol defines the private HTTP contract shared by the ts-skills
// client and registry daemon.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

const (
	JSONMediaType = "application/json"
	ZIPMediaType  = "application/zip"

	maxJSONResponseBytes int64 = 64 << 10
	maxErrorMessageBytes       = 200

	HeaderPublicationNamespace = "X-TS-Skills-Publication-Namespace"
	HeaderPublicationName      = "X-TS-Skills-Publication-Name"
	HeaderPublicationDigest    = "X-TS-Skills-Publication-Digest"

	CodeNotFound       Code = "not_found"
	CodeInvalidRequest Code = "invalid_request"
	CodeTooLarge       Code = "too_large"
	CodeInternal       Code = "internal"
	CodeUnavailable    Code = "unavailable"
)

var (
	ErrInvalid         = errors.New("invalid registry protocol value")
	ErrInvalidResponse = errors.New("invalid registry response")
)

// Code identifies a failure defined by the registry HTTP contract.
type Code string

// Failure is a valid error response returned by the registry. Message is
// bounded and stripped of control characters before a client can observe it.
type Failure struct {
	Code    Code
	Message string
}

func (f *Failure) Error() string {
	if f == nil {
		return "registry request failed"
	}
	return fmt.Sprintf("registry %s: %s", f.Code, f.Message)
}

type CurrentResponse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Digest    string `json:"digest"`
}

type ErrorResponse struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func NewCurrentResponse(publication registry.PublicationID) CurrentResponse {
	return CurrentResponse{
		Namespace: publication.Skill().Namespace().String(),
		Name:      publication.Skill().Name().String(),
		Digest:    publication.Tree().String(),
	}
}

func ParseCurrentResponse(response CurrentResponse) (registry.PublicationID, error) {
	skill, err := ParseSkill(response.Namespace, response.Name)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: current response identity: %v", ErrInvalid, err)
	}
	digest, err := parseDigest(response.Digest)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: current response digest: %v", ErrInvalid, err)
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: current response publication: %v", ErrInvalid, err)
	}
	return publication, nil
}

// ReadCurrent reads and validates a current-publication HTTP response,
// including its media type, bounded strict JSON body, and requested identity.
// A valid non-success response is returned as *Failure.
func ReadCurrent(response *http.Response, expected registry.SkillID) (registry.PublicationID, error) {
	if response == nil || response.Body == nil {
		return registry.PublicationID{}, invalidResponse("current response is missing")
	}
	if response.StatusCode != http.StatusOK {
		return registry.PublicationID{}, ReadFailure(response)
	}
	if err := requireContentType(response.Header.Get("Content-Type"), JSONMediaType, true); err != nil {
		return registry.PublicationID{}, err
	}
	body, err := readBounded(response.Body, response.ContentLength, maxJSONResponseBytes)
	if err != nil {
		return registry.PublicationID{}, invalidResponse("read current response: %v", err)
	}
	var wire CurrentResponse
	if err := decodeStrictJSON(body, &wire); err != nil {
		return registry.PublicationID{}, invalidResponse("decode current response: %v", err)
	}
	publication, err := ParseCurrentResponse(wire)
	if err != nil {
		return registry.PublicationID{}, invalidResponse("parse current response: %v", err)
	}
	if publication.Skill() != expected {
		return registry.PublicationID{}, invalidResponse("current response names another skill")
	}
	return publication, nil
}

// ReadTree validates a successful tree response's media type and publication
// headers without consuming its archive body. A valid non-success response is
// consumed and returned as *Failure.
func ReadTree(response *http.Response, expected registry.PublicationID) error {
	if response == nil || response.Body == nil {
		return invalidResponse("tree response is missing")
	}
	if response.StatusCode != http.StatusOK {
		return ReadFailure(response)
	}
	if err := requireContentType(response.Header.Get("Content-Type"), ZIPMediaType, false); err != nil {
		return err
	}
	publication, err := ParsePublicationHeaders(response.Header)
	if err != nil {
		return invalidResponse("parse publication headers: %v", err)
	}
	if publication != expected {
		return invalidResponse("tree response identifies another publication")
	}
	return nil
}

// ReadFailure consumes and validates a registry error response.
func ReadFailure(response *http.Response) error {
	if response == nil || response.Body == nil {
		return invalidResponse("error response is missing")
	}
	if err := requireContentType(response.Header.Get("Content-Type"), JSONMediaType, true); err != nil {
		return err
	}
	body, err := readBounded(response.Body, response.ContentLength, maxJSONResponseBytes)
	if err != nil {
		return invalidResponse("read error response: %v", err)
	}
	var wire ErrorResponse
	if err := decodeStrictJSON(body, &wire); err != nil {
		return invalidResponse("decode error response: %v", err)
	}
	status, known := StatusForCode(wire.Code)
	if !known || status != response.StatusCode || wire.Message == "" {
		return invalidResponse("unknown error code or status")
	}
	return &Failure{Code: wire.Code, Message: safeMessage(wire.Code, wire.Message)}
}

// WriteCurrent writes the canonical current-publication representation.
func WriteCurrent(w http.ResponseWriter, publication registry.PublicationID) error {
	if w == nil {
		return fmt.Errorf("response writer must be provided")
	}
	w.Header().Set("Content-Type", JSONMediaType)
	return json.NewEncoder(w).Encode(NewCurrentResponse(publication))
}

// WriteFailure writes the canonical representation and status for code.
func WriteFailure(w http.ResponseWriter, code Code, message string) error {
	if w == nil {
		return fmt.Errorf("response writer must be provided")
	}
	status, known := StatusForCode(code)
	if !known {
		code = CodeInternal
		status, _ = StatusForCode(code)
	}
	if message == "" {
		message = defaultMessage(code)
	}
	w.Header().Set("Content-Type", JSONMediaType)
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(ErrorResponse{Code: code, Message: message})
}

func ParseSkill(namespaceText, nameText string) (registry.SkillID, error) {
	namespace, err := registry.ParseNamespace(namespaceText)
	if err != nil || namespace.String() != namespaceText {
		return registry.SkillID{}, fmt.Errorf("%w: noncanonical namespace", ErrInvalid)
	}
	name, err := agentskill.ParseName(nameText)
	if err != nil || name.String() != nameText {
		return registry.SkillID{}, fmt.Errorf("%w: noncanonical Agent Skill name", ErrInvalid)
	}
	skill, err := registry.NewSkillID(namespace, name)
	if err != nil {
		return registry.SkillID{}, fmt.Errorf("%w: skill identity: %v", ErrInvalid, err)
	}
	return skill, nil
}

// ParsePublication parses canonical route values into a publication identity.
func ParsePublication(namespaceText, nameText, digestText string) (registry.PublicationID, error) {
	skill, err := ParseSkill(namespaceText, nameText)
	if err != nil {
		return registry.PublicationID{}, err
	}
	digest, err := parseDigest(digestText)
	if err != nil {
		return registry.PublicationID{}, err
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: publication identity: %v", ErrInvalid, err)
	}
	return publication, nil
}

func ParsePublicationHeaders(header http.Header) (registry.PublicationID, error) {
	namespace, err := requiredHeader(header, HeaderPublicationNamespace)
	if err != nil {
		return registry.PublicationID{}, err
	}
	name, err := requiredHeader(header, HeaderPublicationName)
	if err != nil {
		return registry.PublicationID{}, err
	}
	digestText, err := requiredHeader(header, HeaderPublicationDigest)
	if err != nil {
		return registry.PublicationID{}, err
	}
	skill, err := ParseSkill(namespace, name)
	if err != nil {
		return registry.PublicationID{}, err
	}
	digest, err := parseDigest(digestText)
	if err != nil {
		return registry.PublicationID{}, err
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: publication headers: %v", ErrInvalid, err)
	}
	return publication, nil
}

func SetPublicationHeaders(header http.Header, publication registry.PublicationID) {
	header.Set(HeaderPublicationNamespace, publication.Skill().Namespace().String())
	header.Set(HeaderPublicationName, publication.Skill().Name().String())
	header.Set(HeaderPublicationDigest, publication.Tree().String())
}

func StatusForCode(code Code) (int, bool) {
	switch code {
	case CodeNotFound:
		return http.StatusNotFound, true
	case CodeInvalidRequest:
		return http.StatusBadRequest, true
	case CodeTooLarge:
		return http.StatusRequestEntityTooLarge, true
	case CodeInternal:
		return http.StatusInternalServerError, true
	case CodeUnavailable:
		return http.StatusServiceUnavailable, true
	default:
		return 0, false
	}
}

func invalidResponse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidResponse, fmt.Sprintf(format, args...))
}

func requireContentType(header, expected string, allowUTF8Charset bool) error {
	mediaType, parameters, err := mime.ParseMediaType(header)
	if err != nil || mediaType != expected {
		return invalidResponse("expected Content-Type %s", expected)
	}
	if len(parameters) == 0 {
		return nil
	}
	if allowUTF8Charset && len(parameters) == 1 && strings.EqualFold(parameters["charset"], "utf-8") {
		return nil
	}
	return invalidResponse("unexpected Content-Type parameters")
}

func readBounded(source io.Reader, contentLength, maximum int64) ([]byte, error) {
	if contentLength > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	contents, err := io.ReadAll(io.LimitReader(source, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	if contentLength >= 0 && int64(len(contents)) != contentLength {
		return nil, io.ErrUnexpectedEOF
	}
	return contents, nil
}

func decodeStrictJSON(source []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("response contains trailing JSON data")
	}
	return nil
}

func safeMessage(code Code, message string) string {
	if len(message) > maxErrorMessageBytes || !utf8.ValidString(message) {
		return defaultMessage(code)
	}
	for _, r := range message {
		if unicode.IsControl(r) {
			return defaultMessage(code)
		}
	}
	return message
}

func defaultMessage(code Code) string {
	switch code {
	case CodeNotFound:
		return "requested publication does not exist"
	case CodeInvalidRequest:
		return "registry rejected the request"
	case CodeTooLarge:
		return "registry could not return the tree within its limit"
	case CodeUnavailable:
		return "registry is temporarily unavailable"
	default:
		return "registry encountered an internal error"
	}
}

func requiredHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 || values[0] == "" {
		return "", fmt.Errorf("%w: requires exactly one %s header", ErrInvalid, name)
	}
	return values[0], nil
}

func parseDigest(text string) (registry.TreeDigest, error) {
	digest, err := registry.ParseTreeDigest(text)
	if err != nil || digest.String() != text {
		return registry.TreeDigest{}, fmt.Errorf("%w: noncanonical publication digest", ErrInvalid)
	}
	return digest, nil
}
