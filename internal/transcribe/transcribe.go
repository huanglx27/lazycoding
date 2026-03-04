// Package transcribe provides speech-to-text transcription via pluggable backends.
// Interface and configuration types are defined in pkg/transcribe; this package
// adds the New() factory that wires up concrete implementations.
package transcribe

import (
	"fmt"

	pkgtranscribe "github.com/bishenghua/lazycoding/pkg/transcribe"
)

// Re-export public types as aliases so implementation files in this package
// can reference them without an import path change.
type (
	Transcriber        = pkgtranscribe.Transcriber
	Config             = pkgtranscribe.Config
	WhisperNativeConfig = pkgtranscribe.WhisperNativeConfig
	WhisperPyConfig    = pkgtranscribe.WhisperPyConfig
	WhisperCPPConfig   = pkgtranscribe.WhisperCPPConfig
	GroqConfig         = pkgtranscribe.GroqConfig
)

// New creates a Transcriber from cfg.
// Returns nil (no error) when cfg.Enabled is false.
func New(cfg Config) (Transcriber, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	switch cfg.Backend {
	case "groq":
		if cfg.Groq.APIKey == "" {
			return nil, fmt.Errorf("transcription.groq.api_key is required when backend is \"groq\"")
		}
		model := cfg.Groq.Model
		if model == "" {
			model = "whisper-large-v3-turbo"
		}
		return &groqTranscriber{cfg: cfg.Groq, model: model}, nil

	case "whisper-cpp":
		if cfg.WhisperCPP.Model == "" {
			return nil, fmt.Errorf(
				"transcription.whisper_cpp.model (path to .ggml file) is required when backend is \"whisper-cpp\"\n" +
					"  Download: whisper-download-ggml-model base\n" +
					"  Or visit: https://huggingface.co/ggerganov/whisper.cpp")
		}
		bin := cfg.WhisperCPP.Bin
		if bin == "" {
			bin = "whisper-cli"
		}
		return &whisperCPPTranscriber{cfg: cfg.WhisperCPP, bin: bin}, nil

	case "whisper-native":
		return newWhisperNative(cfg.WhisperNative)

	case "whisper", "openai-whisper", "":
		bin := cfg.WhisperPy.Bin
		if bin == "" {
			bin = "whisper"
		}
		model := cfg.WhisperPy.Model
		if model == "" {
			model = "base"
		}
		return &whisperPyTranscriber{cfg: cfg.WhisperPy, bin: bin, model: model}, nil

	default:
		return nil, fmt.Errorf("unknown transcription backend %q (supported: \"groq\", \"whisper-cpp\", \"whisper-native\", \"whisper\")", cfg.Backend)
	}
}
