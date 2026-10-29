// Package httpapi provides the text-only Chat Completions simulation API.
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yisususu/polyjuice-stream/src/config"
	"github.com/yisususu/polyjuice-stream/src/generation"
	"github.com/yisususu/polyjuice-stream/src/source"
)

type server struct {
	models       map[string]*model
	names        []string
	writeTimeout time.Duration
}

type model struct {
	cfg  config.ModelConfig
	next func() ([]string, error)
}

// New loads every model before returning an immutable registry. Each model owns
// its own content source, even if models refer to the same file or random seed.
func New(cfg config.Config) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	profiles := cfg.Profiles()
	s := &server{models: make(map[string]*model, len(profiles)), writeTimeout: cfg.WriteTimeout}
	for name := range profiles {
		s.names = append(s.names, name)
	}
	sort.Strings(s.names)
	for _, name := range s.names {
		m, err := newModel(profiles[name])
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", name, err)
		}
		s.models[name] = m
	}
	return s, nil
}

func newModel(cfg config.ModelConfig) (*model, error) {
	if cfg.Dataset != "" {
		replay, err := source.LoadJSONL(cfg.Dataset, cfg.TextField, cfg.ChunkChars)
		if err != nil {
			return nil, err
		}
		if err := cfg.ValidateDuration(replay.MaxChunks()); err != nil {
			return nil, err
		}
		return &model{cfg: cfg, next: replay.Next}, nil
	}
	random, err := source.NewRandom(cfg.Seed)
	if err != nil {
		return nil, err
	}
	return &model{cfg: cfg, next: func() ([]string, error) {
		return random.Next(cfg.Chunks, cfg.ChunkChars)
	}}, nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := ""
	switch r.URL.Path {
	case "/health", "/v1/models":
		method = http.MethodGet
	case "/v1/chat/completions":
		method = http.MethodPost
	default:
		s.writeError(w, http.StatusNotFound, invalid("", "Endpoint not found."))
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		s.writeError(w, http.StatusMethodNotAllowed, invalid("", "Method not allowed."))
		return
	}
	switch r.URL.Path {
	case "/health":
		s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/v1/models":
		data := make([]map[string]any, 0, len(s.names))
		for _, name := range s.names {
			data = append(data, map[string]any{"id": name, "object": "model", "created": 0, "owned_by": "polyjuice-stream"})
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   data,
		})
	default:
		s.chat(w, r)
	}
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	req, status, validationErr := readRequest(w, r)
	if validationErr != nil {
		s.writeError(w, status, validationErr)
		return
	}
	m, exists := s.models[req.model]
	if !exists {
		s.writeError(w, http.StatusNotFound, &apiError{
			Message: fmt.Sprintf("Model %q is not configured.", req.model),
			Type:    "invalid_request_error", Param: "model", Code: "model_not_found",
		})
		return
	}
	if req.stream && !canFlush(w) {
		s.writeError(w, http.StatusInternalServerError, internalError("Streaming is unavailable on this HTTP transport."))
		return
	}
	if r.Context().Err() != nil {
		return
	}
	// Selection owns its cursor update. Waiting and transport never hold its lock.
	chunks, err := m.next()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, internalError("Unable to generate response content."))
		return
	}
	plan, err := generation.NewPlan(chunks, m.cfg.FirstDelay, m.cfg.Interval, req.limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, internalError("Unable to plan response content."))
		return
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		s.writeError(w, http.StatusInternalServerError, internalError("Unable to create response ID."))
		return
	}
	meta := metadata{ID: "chatcmpl-" + hex.EncodeToString(id[:]), Created: time.Now().Unix(), Model: req.model}
	if req.stream {
		s.stream(w, r, meta, plan)
		return
	}
	var content strings.Builder
	err = generation.Run(r.Context(), plan, func(chunk string) error {
		content.WriteString(chunk)
		return nil
	})
	if err != nil || r.Context().Err() != nil {
		return
	}
	s.writeJSON(w, http.StatusOK, completion{
		metadata: meta,
		Object:   "chat.completion",
		Choices:  []completeChoice{{Index: 0, Message: message{Role: "assistant", Content: content.String()}, FinishReason: plan.FinishReason}},
	})
}

func (s *server) stream(w http.ResponseWriter, r *http.Request, meta metadata, plan generation.Plan) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Del("Content-Length")
	controller := http.NewResponseController(w)
	first := true
	err := generation.Run(r.Context(), plan, func(chunk string) error {
		delta := map[string]string{"content": chunk}
		if first {
			delta["role"] = "assistant"
			first = false
		}
		return s.writeEvent(w, controller, streamEvent{
			metadata: meta,
			Object:   "chat.completion.chunk",
			Choices:  []streamChoice{{Index: 0, Delta: delta}},
		})
	})
	if err != nil || r.Context().Err() != nil {
		return
	}
	if err := s.writeEvent(w, controller, streamEvent{
		metadata: meta,
		Object:   "chat.completion.chunk",
		Choices:  []streamChoice{{Index: 0, Delta: map[string]string{}, FinishReason: &plan.FinishReason}},
	}); err != nil || r.Context().Err() != nil {
		return
	}
	_ = s.writeFrame(w, controller, []byte("data: [DONE]\n\n"))
}

func (s *server) writeEvent(w http.ResponseWriter, controller *http.ResponseController, event streamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	frame := make([]byte, 0, len(data)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, data...)
	frame = append(frame, '\n', '\n')
	return s.writeFrame(w, controller, frame)
}

func (s *server) writeFrame(w http.ResponseWriter, controller *http.ResponseController, frame []byte) (err error) {
	if err := s.setDeadline(controller); err != nil {
		return err
	}
	defer func() {
		// A write deadline must not become a timeout on the next generation wait.
		clearErr := controller.SetWriteDeadline(time.Time{})
		if err == nil && !errors.Is(clearErr, http.ErrNotSupported) {
			err = clearErr
		}
	}()
	n, err := w.Write(frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return io.ErrShortWrite
	}
	return controller.Flush()
}

func (s *server) setDeadline(controller *http.ResponseController) error {
	err := controller.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	// In-memory recorders may not support deadlines. net/http connections do.
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (s *server) writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	controller := http.NewResponseController(w)
	if err := s.setDeadline(controller); err != nil {
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)+1))
	w.WriteHeader(status)
	if n, err := w.Write(append(data, '\n')); err != nil || n != len(data)+1 {
		return
	}
	// Flush buffered net/http output while the transport deadline is active.
	_ = controller.Flush()
}

func (s *server) writeError(w http.ResponseWriter, status int, err *apiError) {
	s.writeJSON(w, status, struct {
		Error *apiError `json:"error"`
	}{Error: err})
}

func internalError(message string) *apiError {
	return &apiError{Message: message, Type: "server_error", Code: "internal_error"}
}

// Match ResponseController's unwrapping without flushing or committing headers.
func canFlush(w http.ResponseWriter) bool {
	for {
		switch value := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = value.Unwrap()
		default:
			return false
		}
	}
}

type metadata struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
}

type streamEvent struct {
	metadata
	Object  string         `json:"object"`
	Choices []streamChoice `json:"choices"`
}

type streamChoice struct {
	Index        int               `json:"index"`
	Delta        map[string]string `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

type completion struct {
	metadata
	Object  string           `json:"object"`
	Choices []completeChoice `json:"choices"`
}

type completeChoice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
