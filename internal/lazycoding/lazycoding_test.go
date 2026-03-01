package lazycoding

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bishenghua/lazycoding/internal/channel"
	"github.com/bishenghua/lazycoding/internal/config"
)

// mockChannel is a simple channel implementation for testing
type mockChannel struct {
	sentTexts []string
}

func (m *mockChannel) SendText(ctx context.Context, id string, text string) (channel.MessageHandle, error) {
	m.sentTexts = append(m.sentTexts, text)
	return nil, nil
}
func (m *mockChannel) SendTyping(ctx context.Context, id string) error               { return nil }
func (m *mockChannel) AnswerCallback(ctx context.Context, id string, text string) error { return nil }
func (m *mockChannel) SendKeyboard(ctx context.Context, id string, text string, kb [][]channel.KeyboardButton) (channel.MessageHandle, error) {
	return nil, nil
}
func (m *mockChannel) UpdateText(ctx context.Context, handle channel.MessageHandle, text string) error {
	return nil
}
func (m *mockChannel) SendDocument(ctx context.Context, id string, path string, filename string) error {
	return nil
}
func (m *mockChannel) Events(ctx context.Context) <-chan channel.InboundEvent { return nil }

func TestHandleCd(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lazycoding-cd-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create sub dir: %v", err)
	}

	cfg := &config.Config{}
	ch := &mockChannel{}
	lc := New(ch, nil, nil, cfg)

	ctx := context.Background()

	// Test 1: cd into an absolute path
	ev1 := channel.InboundEvent{
		ConversationID: "conv1",
		Command:        "cd",
		CommandArgs:    subDir,
	}
	lc.handleCd(ctx, ev1)
	if lc.currentDir("conv1") != subDir {
		t.Errorf("Expected dir %q, got %q", subDir, lc.currentDir("conv1"))
	}

	// Test 2: cd into a relative path
	ev2 := channel.InboundEvent{
		ConversationID: "conv1",
		Command:        "cd",
		CommandArgs:    "..",
	}
	lc.handleCd(ctx, ev2)
	expectedCleanTmp := filepath.Clean(tmpDir)
	if lc.currentDir("conv1") != expectedCleanTmp {
		t.Errorf("Expected dir %q, got %q", expectedCleanTmp, lc.currentDir("conv1"))
	}

	// Test 3: cd into an invalid path
	ev3 := channel.InboundEvent{
		ConversationID: "conv1",
		Command:        "cd",
		CommandArgs:    "nonexistent",
	}
	lc.handleCd(ctx, ev3)
	// Should not have changed
	if lc.currentDir("conv1") != expectedCleanTmp {
		t.Errorf("Expected dir to remain %q, got %q", expectedCleanTmp, lc.currentDir("conv1"))
	}
}

func TestHandleLs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "lazycoding-ls-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create sub dir: %v", err)
	}

	file1 := filepath.Join(tmpDir, "file1.txt")
	if err := os.WriteFile(file1, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	hiddenFile := filepath.Join(tmpDir, ".hidden")
	if err := os.WriteFile(hiddenFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create hidden file: %v", err)
	}

	cfg := &config.Config{}
	ch := &mockChannel{}
	lc := New(ch, nil, nil, cfg)

	ctx := context.Background()

	// Need to set CWD for testing relative path
	lc.cwdMu.Lock()
	lc.cwd["conv1"] = tmpDir
	lc.cwdMu.Unlock()

	ev := channel.InboundEvent{
		ConversationID: "conv1",
		Command:        "ls",
		CommandArgs:    "",
	}
	lc.handleLs(ctx, ev)

	if len(ch.sentTexts) == 0 {
		t.Fatalf("Expected output, got none")
	}

	output := ch.sentTexts[0]

	// Verify the output formatting
	expectedStrings := []string{
		"📁 <code>" + tmpDir + "</code>",
		"📂 <code>subdir/</code>",
		"📄 <code>file1.txt</code>",
	}

	for _, str := range expectedStrings {
		if !strings.Contains(output, str) {
			t.Errorf("Expected output to contain %q, but got:\n%s", str, output)
		}
	}

	// Should not contain hidden file
	if strings.Contains(output, "📄 <code>.hidden</code>") {
		t.Errorf("Expected output NOT to contain hidden file, but got:\n%s", output)
	}
}

func TestCurrentDir(t *testing.T) {
	cfg := &config.Config{}
	lc := New(nil, nil, nil, cfg)

	dir := lc.currentDir("conv1")
	if dir != "" {
		t.Errorf("Expected empty dir, got %q", dir)
	}

	lc.cwdMu.Lock()
	lc.cwd["conv1"] = "/tmp/foo"
	lc.cwdMu.Unlock()

	dir = lc.currentDir("conv1")
	if dir != "/tmp/foo" {
		t.Errorf("Expected /tmp/foo, got %q", dir)
	}
}
