// Package protocol implements the maildir request/response protocol (plan §3,
// §4): the on-sandbox tree, the request/response envelope, atomic delivery and
// pickup, and the dispatcher that services requests over a remotefs.FS.
package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion is the envelope version Popo writes and expects.
const SchemaVersion = 1

// Response.Status values (plan §4).
const (
	StatusOK                 = "ok"
	StatusError              = "error"
	StatusNeedsClarification = "needs_clarification"
)

// Request is an outbox envelope (plan §4).
type Request struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	CreatedAt     time.Time       `json:"created_at"`
	Params        json.RawMessage `json:"params,omitempty"`
}

// Response is an inbox envelope, sharing the request's id (plan §4).
type Response struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Status        string          `json:"status"`
	CompletedAt   time.Time       `json:"completed_at"`
	Result        json.RawMessage `json:"result,omitempty"`
	Error         *Error          `json:"error,omitempty"`
	Clarification *Clarification  `json:"clarification,omitempty"`
}

// Error describes why Popo could not service a request.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Clarification carries a question back to Nana (status=needs_clarification).
type Clarification struct {
	Question string `json:"question"`
}

// OK builds a successful response with result marshaled into Result. Exported
// for handlers in other packages (e.g. python.install).
func OK(id string, result any) Response {
	raw, err := json.Marshal(result)
	if err != nil {
		return Errf(id, "internal", "marshal result: %v", err)
	}
	return Response{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Status:        StatusOK,
		CompletedAt:   time.Now().UTC(),
		Result:        raw,
	}
}

// NeedsClarification builds a held response that asks the operator/Nana a
// question (e.g. controller-venue egress awaiting approval, §6.1).
func NeedsClarification(id, question string) Response {
	return Response{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Status:        StatusNeedsClarification,
		CompletedAt:   time.Now().UTC(),
		Clarification: &Clarification{Question: question},
	}
}

// Errf builds an error response (Popo failed to service the request).
func Errf(id, code, format string, args ...any) Response {
	return Response{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Status:        StatusError,
		CompletedAt:   time.Now().UTC(),
		Error:         &Error{Code: code, Message: fmt.Sprintf(format, args...)},
	}
}
