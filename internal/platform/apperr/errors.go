package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Status     int
	Code       string
	Title      string
	Detail     string
	Extensions map[string]any
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Title)
	}
	return fmt.Sprintf("%s: %s: %s", e.Code, e.Title, e.Detail)
}

func New(status int, code, title, detail string) *Error {
	return &Error{Status: status, Code: code, Title: title, Detail: detail}
}

func (e *Error) With(key string, value any) *Error {
	if e.Extensions == nil {
		e.Extensions = make(map[string]any)
	}
	e.Extensions[key] = value
	return e
}

func BadRequest(code, detail string) *Error {
	return New(http.StatusBadRequest, code, "Bad request", detail)
}

func NotFound(code, detail string) *Error {
	return New(http.StatusNotFound, code, "Not found", detail)
}

func Conflict(code, title, detail string) *Error {
	return New(http.StatusConflict, code, title, detail)
}

func Unprocessable(code, detail string) *Error {
	return New(http.StatusUnprocessableEntity, code, "Unprocessable entity", detail)
}

func Internal(detail string) *Error {
	return New(http.StatusInternalServerError, "internal_error", "Internal server error", detail)
}

func As(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
