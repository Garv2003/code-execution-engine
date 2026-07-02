package sandbox_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/garv2003/code-execution-engine/internal/sandbox"
)

func TestLoadLanguageConfig_Valid(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "languages-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	config := sandbox.LanguageConfig{
		"python": {
			Image:    "python:3.10-slim",
			Filename: "solution.py",
			RunCommand: "python3 /app/solution.py",
		},
	}

	data, _ := json.Marshal(config)
	_, _ = tmpFile.Write(data)
	tmpFile.Close()

	loaded, err := sandbox.LoadLanguageConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(loaded) != 1 {
		t.Errorf("expected 1 language, got %d", len(loaded))
	}

	spec, exists := loaded["python"]
	if !exists {
		t.Fatal("expected python language spec")
	}

	if spec.Image != "python:3.10-slim" {
		t.Errorf("expected python:3.10-slim, got %s", spec.Image)
	}
}

func TestLoadLanguageConfig_FileNotFound(t *testing.T) {
	_, err := sandbox.LoadLanguageConfig("/nonexistent/path.json")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestLoadLanguageConfig_InvalidJSON(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "languages-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	_, _ = tmpFile.WriteString("{invalid json}")
	tmpFile.Close()

	_, err = sandbox.LoadLanguageConfig(tmpFile.Name())
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
