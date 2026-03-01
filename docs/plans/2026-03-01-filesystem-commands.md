# Filesystem Navigation Commands Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement Unix-style filesystem commands (`/pwd`, `/cd`, `/ls`, `/new`) for lazycoding chat channels.

**Architecture:** Maintain a runtime `cwd` map per-conversation in the `Lazycoding` struct. Default to the configured `work_dir`. Intercept `/pwd`, `/cd`, `/ls`, `/new` slash commands in `handleCommand` to manipulate this map, read directories, and reset sessions.

**Tech Stack:** Go (1.21+), standard library `os`, `path/filepath`, `strings`, `sync`.

---

### Task 1: Add CWD State Management

**Files:**
- Modify: `internal/lazycoding/lazycoding.go`
- Modify: `internal/lazycoding/lazycoding_test.go` (if exists, or create)

**Step 1: Write the failing test**

```go
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
```

**Step 2: Run test to verify it fails**

Run: `go test -run TestCurrentDir ./internal/lazycoding`
Expected: FAIL (method/fields not defined)

**Step 3: Write minimal implementation**

Add `cwd map[string]string` and `cwdMu sync.RWMutex` to `Lazycoding` struct.
Initialize `cwd` map in `New()`.
Implement `currentDir(convID string) string`.

**Step 4: Run test to verify it passes**

Run: `go test -run TestCurrentDir ./internal/lazycoding`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/lazycoding/
git commit -m "feat: add cwd state management to lazycoding struct"
```

---

### Task 2: Switch `dispatch` to use dynamic CWD

**Files:**
- Modify: `internal/lazycoding/lazycoding.go`

**Step 1: Implement minimal code**

In `dispatch()` method, find `workDir := lc.cfg.WorkDirFor(ev.ConversationID)`.
Replace with `workDir := lc.currentDir(ev.ConversationID)`.

**Step 2: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/lazycoding/
git commit -m "refactor: use runtime cwd in dispatch"
```

---

### Task 3: Implement `/pwd` command

**Files:**
- Modify: `internal/lazycoding/lazycoding.go`

**Step 1: Implement minimal code**

In `handleCommand()`, add `case "pwd":`:
```go
dir := lc.currentDir(convID)
if dir == "" {
    dir = "(lazycoding launch directory)"
}
lc.ch.SendText(ctx, convID, "Current directory: <code>"+tgrender.EscapeHTML(dir)+"</code>") //nolint:errcheck
```
Add to `/help` text: `/pwd        – show current directory (set by /cd)`

**Step 2: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/lazycoding/
git commit -m "feat: add /pwd command and update help"
```

---

### Task 4: Implement `/cd` command

**Files:**
- Modify: `internal/lazycoding/lazycoding.go`

**Step 1: Implement minimal code**

Add `case "cd":` calling `lc.handleCd(ctx, ev)`.
Implement `handleCd()`:
1. Parse `ev.CommandArgs`. If empty or `~`, use `os.UserHomeDir()`.
2. Support `~/path`.
3. Join with `lc.currentDir(convID)` if relative.
4. Clean path with `filepath.Clean()`.
5. Check `os.Stat(path)`. If err or not dir, send error message.
6. Success: `lc.cwdMu.Lock(); lc.cwd[convID] = path; lc.cwdMu.Unlock()`.
7. Send success message.

Update `/help` text.

**Step 2: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/lazycoding/
git commit -m "feat: add /cd command for dynamic directory switching"
```

---

### Task 5: Implement `/ls` command

**Files:**
- Modify: `internal/lazycoding/lazycoding.go`

**Step 1: Implement minimal code**

Add `case "ls":` calling `lc.handleLs(ctx, ev)`.
Implement `handleLs()`:
1. Determine path (args or `lc.currentDir(convID)`).
2. Clean and check `os.Stat()`.
3. `os.ReadDir()`.
4. Filter out `.` prefixed (hidden) files.
5. Sort: Dirs first, then files, alphabetically.
6. Format output (up to 50 items). Append `/` to dirs. Use `📁` for current dir, `📂` for dirs, `📄` for files.
7. Send text.

Update `/help` text.

**Step 2: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/lazycoding/
git commit -m "feat: add /ls command to list directory contents"
```

---

### Task 6: Implement `/new` command

**Files:**
- Modify: `internal/lazycoding/lazycoding.go`

**Step 1: Implement minimal code**

In `handleCommand()`, add `case "new":`:
```go
lc.store.Delete(lc.sessionKey(convID))
lc.cancelConversation(convID)
dir := lc.currentDir(convID)
if dir == "" {
    dir = "(lazycoding launch directory)"
}
lc.ch.SendText(ctx, convID, "✨ Started a new session in:\n<code>"+tgrender.EscapeHTML(dir)+"</code>\n\nJust send a message to begin.") //nolint:errcheck
```

Update `/help` text.

**Step 2: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/lazycoding/
git commit -m "feat: add /new command for creating a session in the current directory"
```
