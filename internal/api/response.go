package api

import (
	"encoding/json"
	"fmt"
	"github.com/phillarmonic/syncopate-db/internal/errors"
	"github.com/sirupsen/logrus"
	"net/http"
)

// ErrorResponse represents an error response to the API
type ErrorResponse struct {
	Error   string           `json:"error"`
	Message string           `json:"message,omitempty"`
	Code    int              `json:"code"`
	DBCode  errors.ErrorCode `json:"db_code"`
}

// respondWithError sends an error response with the given status code and message
func (s *Server) respondWithError(w http.ResponseWriter, code int, message string, err error) {
	// Extract DB error code if available, or map from HTTP code
	var dbCode errors.ErrorCode
	if err != nil {
		dbCode = errors.GetErrorCode(err)
	} else {
		dbCode = errors.MapHTTPError(code)
	}

	errorResponse := ErrorResponse{
		Error:   http.StatusText(code),
		Message: message,
		Code:    code,
		DBCode:  dbCode,
	}
	s.respondWithJSON(w, code, errorResponse, true)

	// Log the error with full details
	s.logger.WithFields(logrus.Fields{
		"status_code": code,
		"db_code":     string(dbCode),
		"message":     message,
		"error":       err,
	}).Error("API Error")
}

// Version that keeps backwards compatibility with existing code
func (s *Server) respondWithSimpleError(w http.ResponseWriter, code int, message string) {
	s.respondWithError(w, code, message, nil)
}

// respondWithJSON sends a JSON response with the given status code and data
func (s *Server) respondWithJSON(w http.ResponseWriter, code int, data interface{}, prettyPrint ...bool) {
	// Set headers
	w.Header().Set("Content-Type", "application/json")

	// Marshal data to JSON, with optional pretty printing
	if data != nil {
		var response []byte
		var err error

		// Check if pretty printing is requested
		isPrettyPrint := false
		if len(prettyPrint) > 0 && prettyPrint[0] {
			isPrettyPrint = true
		}

		if isPrettyPrint {
			// Pretty print with indentation
			response, err = json.MarshalIndent(data, "", "  ")
		} else {
			// Regular compact JSON
			response, err = json.Marshal(data)
		}

		if err != nil {
			s.logger.Errorf("Error marshaling JSON response: %v", err)
			// Set status code before writing error
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"Internal Server Error","db_code":"SY002"}`))
			return
		}

		// Set the content length header
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(response)))

		// Set the status code
		w.WriteHeader(code)

		// Write response
		if _, err := w.Write(response); err != nil {
			s.logger.Errorf("Error writing JSON response: %v", err)
		}
	} else {
		// No data, just set the status code
		w.WriteHeader(code)
	}
}
