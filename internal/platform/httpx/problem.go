package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/oliaditya05/stockflow/internal/platform/apperr"
)

const ProblemContentType = "application/problem+json"

type Problem struct {
	Type       string         `json:"type"`
	Title      string         `json:"title"`
	Status     int            `json:"status"`
	Detail     string         `json:"detail,omitempty"`
	Instance   string         `json:"instance,omitempty"`
	Code       string         `json:"code,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

func NewProblem(err *apperr.Error, instance string) Problem {
	problemType := "about:blank"
	if err.Code != "" {
		problemType = "https://stockflow.local/problems/" + err.Code
	}
	return Problem{
		Type:       problemType,
		Title:      err.Title,
		Status:     err.Status,
		Detail:     err.Detail,
		Instance:   instance,
		Code:       err.Code,
		Extensions: err.Extensions,
	}
}

func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func WriteProblem(w http.ResponseWriter, r *http.Request, err *apperr.Error) {
	problem := NewProblem(err, r.URL.Path)
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(err.Status)
	_ = json.NewEncoder(w).Encode(problem)
}

func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	if domain, ok := apperr.As(err); ok {
		WriteProblem(w, r, domain)
		return
	}
	if logger != nil {
		logger.ErrorContext(r.Context(), "unhandled error", slog.Any("error", err), slog.String("path", r.URL.Path))
	}
	WriteProblem(w, r, apperr.Internal("unexpected error"))
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		WriteProblem(w, r, apperr.BadRequest("invalid_json", "request body must be valid JSON: "+err.Error()))
		return false
	}
	return true
}

func MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, apperr.New(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", r.Method+" is not supported for "+r.URL.Path))
}
