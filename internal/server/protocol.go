package server

const apiVersion = "v1"

const (
	headerPublicationNamespace = "X-TS-Skills-Publication-Namespace"
	headerPublicationName      = "X-TS-Skills-Publication-Name"
	headerPublicationDigest    = "X-TS-Skills-Publication-Digest"
)

type currentResponse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Digest    string `json:"digest"`
}
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	codeNotFound       = "not_found"
	codeInvalidRequest = "invalid_request"
	codeTooLarge       = "too_large"
	codeInternal       = "internal"
)

func statusForCode(code string) (int, bool) {
	switch code {
	case codeNotFound:
		return 404, true
	case codeInvalidRequest:
		return 400, true
	case codeTooLarge:
		return 413, true
	case codeInternal:
		return 500, true
	}
	return 0, false
}
