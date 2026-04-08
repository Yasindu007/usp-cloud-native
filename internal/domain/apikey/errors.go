package apikey

import "errors"

var (
	ErrNotFound       = errors.New("apikey: not found")
	ErrAlreadyRevoked = errors.New("apikey: key has already been revoked")
	ErrExpired        = errors.New("apikey: key has expired")
	ErrRevoked        = errors.New("apikey: key has been revoked")
	ErrInvalidKey     = errors.New("apikey: key is invalid")
	ErrNameRequired   = errors.New("apikey: name is required")
	ErrInvalidScope   = errors.New("apikey: invalid scope value")
)
