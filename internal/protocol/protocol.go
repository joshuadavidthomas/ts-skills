package protocol

import "errors"

const Version = "v1"

const (
	HeaderPublicationNamespace = "X-TS-Skills-Publication-Namespace"
	HeaderPublicationName      = "X-TS-Skills-Publication-Name"
	HeaderPublicationDigest    = "X-TS-Skills-Publication-Digest"
)

type CurrentResponse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Digest    string `json:"digest"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	CodeNotFound       = "not_found"
	CodeInvalidRequest = "invalid_request"
	CodeTooLarge       = "too_large"
	CodeInternal       = "internal"
)

var ErrProtocol = errors.New("invalid registry protocol response")

func StatusForCode(code string) (int, bool) {
	switch code {
	case CodeNotFound:
		return 404, true
	case CodeInvalidRequest:
		return 400, true
	case CodeTooLarge:
		return 413, true
	case CodeInternal:
		return 500, true
	default:
		return 0, false
	}
}
