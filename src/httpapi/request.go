package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"unicode/utf8"
)

// MaxRequestBytes bounds both decoding work and memory for incoming requests.
const MaxRequestBytes = 1 << 20

type chatRequest struct {
	model  string
	stream bool
	limit  int
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code"`
}

func invalid(param, message string) *apiError {
	return &apiError{Message: message, Type: "invalid_request_error", Param: param, Code: "invalid_request"}
}

func readRequest(w http.ResponseWriter, r *http.Request) (chatRequest, int, *apiError) {
	var req chatRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	if err != nil {
		if _, ok := err.(*http.MaxBytesError); ok {
			return req, http.StatusRequestEntityTooLarge, invalid("", "Request body exceeds 1 MiB.")
		}
		return req, http.StatusBadRequest, invalid("", "Unable to read request body.")
	}
	var fields map[string]json.RawMessage
	if !utf8.Valid(body) || json.Unmarshal(body, &fields) != nil || fields == nil {
		return req, http.StatusBadRequest, invalid("", "Request body must be a single valid JSON object.")
	}
	if !stringValue(fields["model"], &req.model) || req.model == "" {
		return req, http.StatusBadRequest, invalid("model", "model must be a non-empty string.")
	}
	if err := validateMessages(fields["messages"]); err != nil {
		return req, http.StatusBadRequest, err
	}
	if raw, exists := fields["stream"]; exists && !boolValue(raw, &req.stream) {
		return req, http.StatusBadRequest, invalid("stream", "stream must be a boolean.")
	}
	for _, name := range []string{"tools", "tool_choice", "functions", "function_call", "response_format", "stop", "modalities", "audio", "prediction"} {
		if _, exists := fields[name]; exists {
			return req, http.StatusBadRequest, invalid(name, name+" is not supported; only plain text responses are available.")
		}
	}
	for _, name := range []string{"max_tokens", "max_completion_tokens"} {
		if raw, exists := fields[name]; exists {
			var limit int
			if !intValue(raw, &limit) || limit <= 0 {
				return req, http.StatusBadRequest, invalid(name, name+" must be a positive integer of simulated output chunks.")
			}
			if req.limit != 0 && req.limit != limit {
				return req, http.StatusBadRequest, invalid(name, "max_tokens and max_completion_tokens must agree when both are provided.")
			}
			req.limit = limit
		}
	}
	if raw, exists := fields["n"]; exists {
		var n int
		if !intValue(raw, &n) || n != 1 {
			return req, http.StatusBadRequest, invalid("n", "Only n: 1 is supported.")
		}
	}
	for _, name := range []string{"temperature", "top_p", "presence_penalty", "frequency_penalty"} {
		if raw, exists := fields[name]; exists {
			var value float64
			if isNull(raw) || json.Unmarshal(raw, &value) != nil || math.IsInf(value, 0) || math.IsNaN(value) {
				return req, http.StatusBadRequest, invalid(name, name+" must be a number; its value is ignored.")
			}
		}
	}
	if raw, exists := fields["seed"]; exists {
		var seed int64
		if isNull(raw) || json.Unmarshal(raw, &seed) != nil {
			return req, http.StatusBadRequest, invalid("seed", "seed must be an integer; use the server seed option for reproducible responses.")
		}
	}
	if raw, exists := fields["stream_options"]; exists {
		var options map[string]json.RawMessage
		if json.Unmarshal(raw, &options) != nil || options == nil {
			return req, http.StatusBadRequest, invalid("stream_options", "stream_options must be an object.")
		}
		for key, value := range options {
			var include bool
			if key != "include_usage" {
				return req, http.StatusBadRequest, invalid("stream_options."+key, "Only stream_options.include_usage is supported.")
			}
			if !boolValue(value, &include) {
				return req, http.StatusBadRequest, invalid("stream_options.include_usage", "include_usage must be a boolean; usage accounting is omitted.")
			}
		}
	}
	return req, 0, nil
}

func validateMessages(raw json.RawMessage) *apiError {
	var messages []map[string]json.RawMessage
	if json.Unmarshal(raw, &messages) != nil || len(messages) == 0 {
		return invalid("messages", "messages must be a non-empty array of text messages.")
	}
	for i, message := range messages {
		prefix := fmt.Sprintf("messages[%d]", i)
		var role, content string
		if !stringValue(message["role"], &role) {
			return invalid(prefix+".role", "Each message must have a string role.")
		}
		switch role {
		case "system", "developer", "user", "assistant":
		default:
			return invalid(prefix+".role", "Supported roles are system, developer, user and assistant.")
		}
		if !stringValue(message["content"], &content) {
			return invalid(prefix+".content", "Message content must be a string; multimodal input is not supported.")
		}
		for _, field := range []string{"tool_calls", "tool_call_id", "function_call", "audio"} {
			if _, exists := message[field]; exists {
				return invalid(prefix+"."+field, field+" is not supported.")
			}
		}
	}
	return nil
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func stringValue(raw json.RawMessage, dst *string) bool {
	return !isNull(raw) && json.Unmarshal(raw, dst) == nil
}

func boolValue(raw json.RawMessage, dst *bool) bool {
	return !isNull(raw) && json.Unmarshal(raw, dst) == nil
}

func intValue(raw json.RawMessage, dst *int) bool {
	return !isNull(raw) && json.Unmarshal(raw, dst) == nil
}
