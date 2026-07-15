// Package api implements the DeDi read plane per docs/design.md Addendum B.
package api

import (
	"encoding/json"
	"log"
	"net/http"
)

type envelope struct {
	Message string `json:"message"`
	Data    any    `json:"data"`
	Proof   any    `json:"proof,omitempty"` // dedid extension, additive
}

type errorBody struct {
	Message string `json:"message"`
	Error   string `json:"error"`
	Code    string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}

func ok(w http.ResponseWriter, message string, data any) {
	writeJSON(w, http.StatusOK, envelope{Message: message, Data: data})
}

func okProof(w http.ResponseWriter, message string, data, proof any) {
	writeJSON(w, http.StatusOK, envelope{Message: message, Data: data, Proof: proof})
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Message: "An error occurred", Error: msg, Code: code})
}

func notFound(w http.ResponseWriter, what string) {
	writeErr(w, http.StatusNotFound, "NOT_FOUND", what+" not found")
}

func badRequest(w http.ResponseWriter, msg string) {
	writeErr(w, http.StatusBadRequest, "INVALID_REQUEST", msg)
}

func internal(w http.ResponseWriter, err error) {
	log.Printf("api: internal error: %v", err)
	writeErr(w, http.StatusInternalServerError, "INTERNAL", "internal server error")
}
