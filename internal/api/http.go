package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

type apiError struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errBadRequest(msg string, fields map[string]string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: msg, Fields: fields}
}
func errUnauthorized(msg string) *apiError {
	return &apiError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: msg}
}
func errForbidden(msg string) *apiError {
	return &apiError{Status: http.StatusForbidden, Code: "forbidden", Message: msg}
}
func errNotFound() *apiError {
	return &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "not found"}
}
func errConflict(msg string) *apiError {
	return &apiError{Status: http.StatusConflict, Code: "conflict", Message: msg}
}

type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return nil
	}
	return json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e *apiError) {
	_ = writeJSON(w, e.Status, errorBody{Code: e.Code, Message: e.Message, Fields: e.Fields})
}

// call is one in-flight request.
type call struct {
	w     http.ResponseWriter
	r     *http.Request
	s     *Server
	actor *Actor // nil on public routes
}

func (c *call) json(status int, v any) error { return writeJSON(c.w, status, v) }

func (c *call) noContent() error {
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// decode reads a JSON body into v, rejecting unknown fields and trailing data.
func (c *call) decode(v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(c.w, c.r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errBadRequest("request body is required", nil)
		}
		return errBadRequest("invalid JSON: "+err.Error(), nil)
	}
	if dec.More() {
		return errBadRequest("unexpected data after JSON body", nil)
	}
	return nil
}

// decodeOptional is decode but tolerates an empty body (v keeps zero values).
func (c *call) decodeOptional(v any) error {
	if c.r.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.w, c.r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return errBadRequest("invalid JSON: "+err.Error(), nil)
	}
	return nil
}

func (c *call) pathInt(name string) (int64, error) {
	n, err := strconv.ParseInt(c.r.PathValue(name), 10, 64)
	if err != nil || n < 1 {
		return 0, errBadRequest("invalid "+name, map[string]string{name: "must be a positive integer"})
	}
	return n, nil
}

// paging parses limit (default 50, max 200) and offset.
func (c *call) paging() (limit, offset int, err error) {
	limit, offset = 50, 0
	q := c.r.URL.Query()
	fields := map[string]string{}
	if v := q.Get("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 200 {
			fields["limit"] = "must be an integer between 1 and 200"
		} else {
			limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 {
			fields["offset"] = "must be a non-negative integer"
		} else {
			offset = n
		}
	}
	if len(fields) > 0 {
		return 0, 0, errBadRequest("invalid paging parameters", fields)
	}
	return limit, offset, nil
}

func ts(t time.Time) time.Time { return t.UTC() }

func tsp(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func mustReason(s string) error {
	if len(s) > 500 {
		return errBadRequest("reason is too long", map[string]string{"reason": "must be at most 500 characters"})
	}
	return nil
}

func fullName(r model.Repo) string { return fmt.Sprintf("%s/%s", r.Owner, r.Name) }

func isConflict(err error) bool { return errors.Is(err, model.ErrConflict) }

func jsonObj(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
