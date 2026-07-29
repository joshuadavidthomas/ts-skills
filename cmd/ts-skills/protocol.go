package main

import "errors"

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

var (
	errProtocol       = errors.New("invalid registry protocol response")
	errNotFound       = errors.New("registry value not found")
	errInvalidRequest = errors.New("invalid registry request")
	errInternal       = errors.New("registry internal error")
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
	default:
		return 0, false
	}
}
