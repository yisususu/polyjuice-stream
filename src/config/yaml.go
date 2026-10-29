package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

const MaxConfigBytes = 1024 * 1024

// Load reads one bounded YAML document. Dataset paths are relative to this file.
// It validates configuration; the server loads all datasets before listening.
func Load(path string) (Config, error) {
	reader := yamlConfig{path: path}
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, reader.fail(nil, "cannot access configuration: %v", err)
	}
	if !info.Mode().IsRegular() {
		return Config{}, reader.fail(nil, "configuration must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, reader.fail(nil, "cannot open configuration: %v", err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Config{}, reader.fail(nil, "configuration must be a readable regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return Config{}, reader.fail(nil, "cannot read configuration: %v", err)
	}
	if len(data) > MaxConfigBytes {
		return Config{}, reader.fail(nil, "configuration exceeds %d bytes", MaxConfigBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return Config{}, reader.fail(nil, "cannot decode YAML: %v", err)
	}
	if len(document.Content) != 1 {
		return Config{}, reader.fail(&document, "configuration must contain one YAML mapping")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Config{}, reader.fail(nil, "cannot decode trailing YAML: %v", err)
		}
		return Config{}, reader.fail(&extra, "only one YAML document is supported")
	}
	root := document.Content[0]
	fields, err := reader.mapping(root, "configuration", "listen", "write_timeout", "models")
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	if node := fields["listen"]; node != nil {
		cfg.Listen, err = reader.string(node, "listen")
		if err != nil {
			return Config{}, err
		}
	}
	if node := fields["write_timeout"]; node != nil {
		cfg.WriteTimeout, err = reader.duration(node, "write_timeout")
		if err != nil {
			return Config{}, err
		}
	}
	models := fields["models"]
	if models == nil {
		return Config{}, reader.fail(root, "models is required")
	}
	profiles, err := reader.mapping(models, "models")
	if err != nil {
		return Config{}, err
	}
	if len(profiles) == 0 {
		return Config{}, reader.fail(models, "models must contain at least one model")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, reader.fail(root, "cannot resolve configuration path: %v", err)
	}
	cfg.Models = make(map[string]ModelConfig, len(profiles))
	// Preserve document order when reporting the first invalid model.
	for i := 0; i < len(models.Content); i += 2 {
		key, value := models.Content[i], models.Content[i+1]
		if err := validateModelName(key.Value); err != nil {
			return Config{}, reader.fail(key, "%v", err)
		}
		model, err := reader.model(value, key.Value, filepath.Dir(absolute))
		if err != nil {
			return Config{}, err
		}
		cfg.Models[key.Value] = model
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, reader.fail(root, "%v", err)
	}
	return cfg, nil
}

type yamlConfig struct{ path string }

func (r yamlConfig) fail(node *yaml.Node, format string, args ...any) error {
	line := 1
	if node != nil && node.Line > 0 {
		line = node.Line
	}
	return fmt.Errorf("%s:%d: %s", r.path, line, fmt.Sprintf(format, args...))
}

// Validate keys before decoding values so duplicate names cannot overwrite state.
// Aliases and merge keys are deliberately outside this small configuration schema.
func (r yamlConfig) mapping(node *yaml.Node, label string, allowed ...string) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, r.fail(node, "%s must be a mapping", label)
	}
	fields := make(map[string]*yaml.Node, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, r.fail(key, "%s keys must be strings; aliases and merge keys are unsupported", label)
		}
		if _, duplicate := fields[key.Value]; duplicate {
			return nil, r.fail(key, "duplicate %s key %q", label, key.Value)
		}
		if len(allowed) > 0 {
			known := false
			for _, name := range allowed {
				known = known || name == key.Value
			}
			if !known {
				return nil, r.fail(key, "unknown %s field %q", label, key.Value)
			}
		}
		fields[key.Value] = value
	}
	return fields, nil
}

func (r yamlConfig) string(node *yaml.Node, label string) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", r.fail(node, "%s must be a string", label)
	}
	return node.Value, nil
}

func (r yamlConfig) duration(node *yaml.Node, label string) (time.Duration, error) {
	value, err := r.string(node, label)
	if err != nil {
		return 0, err
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, r.fail(node, "%s must be a duration such as 0s, 30ms or 1s", label)
	}
	return duration, nil
}

func (r yamlConfig) integer(node *yaml.Node, label string, target any) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" || node.Decode(target) != nil {
		return r.fail(node, "%s must be an integer within its supported range", label)
	}
	return nil
}

func (r yamlConfig) model(node *yaml.Node, name, directory string) (ModelConfig, error) {
	label := fmt.Sprintf("model %q", name)
	fields, err := r.mapping(node, label, "source", "seed", "chunks", "chunk_chars", "first_delay", "interval", "path", "text_field")
	if err != nil {
		return ModelConfig{}, err
	}
	sourceNode := fields["source"]
	if sourceNode == nil {
		return ModelConfig{}, r.fail(node, "%s requires source: random or source: jsonl", label)
	}
	source, err := r.string(sourceNode, label+" source")
	if err != nil {
		return ModelConfig{}, err
	}
	model := DefaultModel()
	switch source {
	case "random":
		for _, key := range []string{"path", "text_field"} {
			if value := fields[key]; value != nil {
				return ModelConfig{}, r.fail(value, "%s: %s is only supported for source: jsonl", label, key)
			}
		}
		if value := fields["chunks"]; value != nil {
			if err := r.integer(value, label+" chunks", &model.Chunks); err != nil {
				return ModelConfig{}, err
			}
		}
		if value := fields["seed"]; value != nil {
			var seed int64
			if err := r.integer(value, label+" seed", &seed); err != nil {
				return ModelConfig{}, err
			}
			model.Seed = &seed
		}
	case "jsonl":
		for _, key := range []string{"chunks", "seed"} {
			if value := fields[key]; value != nil {
				return ModelConfig{}, r.fail(value, "%s: %s is only supported for source: random", label, key)
			}
		}
		path := fields["path"]
		if path == nil {
			return ModelConfig{}, r.fail(node, "%s requires a JSONL path", label)
		}
		model.Dataset, err = r.string(path, label+" path")
		if err != nil {
			return ModelConfig{}, err
		}
		if model.Dataset == "" {
			return ModelConfig{}, r.fail(path, "%s path must not be empty", label)
		}
		if !filepath.IsAbs(model.Dataset) {
			model.Dataset = filepath.Join(directory, model.Dataset)
		}
		if value := fields["text_field"]; value != nil {
			model.TextField, err = r.string(value, label+" text_field")
			if err != nil {
				return ModelConfig{}, err
			}
		}
	default:
		return ModelConfig{}, r.fail(sourceNode, "%s source must be random or jsonl", label)
	}
	if value := fields["chunk_chars"]; value != nil {
		if err := r.integer(value, label+" chunk_chars", &model.ChunkChars); err != nil {
			return ModelConfig{}, err
		}
	}
	for _, field := range []struct {
		name   string
		target *time.Duration
	}{{"first_delay", &model.FirstDelay}, {"interval", &model.Interval}} {
		if value := fields[field.name]; value != nil {
			*field.target, err = r.duration(value, label+" "+field.name)
			if err != nil {
				return ModelConfig{}, err
			}
		}
	}
	if err := model.Validate(); err != nil {
		return ModelConfig{}, r.fail(node, "%s: %v", label, err)
	}
	return model, nil
}
