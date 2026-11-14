package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TwiN/deepmerge"
)

var (
	ErrEditorConflict        = errors.New("configuration changed on disk; reload before saving")
	ErrEditorFile            = errors.New("unknown configuration file")
	configurationEditorMutex sync.Mutex
)

type EditorFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	path    string
}

type EditorSnapshot struct {
	Files    []EditorFile `json:"files"`
	Revision string       `json:"revision"`
}

// EditorEnabled is intentionally opt-in: raw YAML can contain credentials.
func EditorEnabled() bool {
	return os.Getenv("GATUS_CONFIG_EDITOR") == "true"
}

func (config *Config) EditorSnapshot() (*EditorSnapshot, error) {
	configurationEditorMutex.Lock()
	defer configurationEditorMutex.Unlock()
	return config.editorSnapshot()
}

func (config *Config) editorSnapshot() (*EditorSnapshot, error) {
	root, err := filepath.EvalSymlinks(config.configPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	snapshot := &EditorSnapshot{Files: []EditorFile{}}
	add := func(path, name string) error {
		// Directory configuration symlinks could lead outside the configured tree.
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("configuration editor requires regular files")
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot.Files = append(snapshot.Files, EditorFile{Name: filepath.ToSlash(name), Content: string(content), path: path})
		return nil
	}
	if info.IsDir() {
		err = walkConfigDir(root, func(path string, _ fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			name, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			return add(path, name)
		})
	} else {
		err = add(root, filepath.Base(root))
	}
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	for _, file := range snapshot.Files {
		fmt.Fprintf(hash, "%d:%s%d:%s", len(file.Name), file.Name, len(file.Content), file.Content)
	}
	snapshot.Revision = hex.EncodeToString(hash.Sum(nil))
	return snapshot, nil
}

// EditConfiguration validates the full merged candidate using startup rules,
// then optionally writes just the selected file. It never mutates the live config.
func (config *Config) EditConfiguration(name, content, revision string, save bool) (result *EditorSnapshot, err error) {
	configurationEditorMutex.Lock()
	defer configurationEditorMutex.Unlock()
	// Some startup validators assume non-nil entries. Malformed drafts must
	// produce a validation response rather than panic the running server.
	defer func() {
		if recover() != nil {
			result, err = nil, errors.New("invalid configuration structure")
		}
	}()
	snapshot, err := config.editorSnapshot()
	if err != nil {
		return nil, err
	}
	if revision == "" || revision != snapshot.Revision {
		return nil, ErrEditorConflict
	}
	var selected *EditorFile
	for i := range snapshot.Files {
		if snapshot.Files[i].Name == name {
			selected = &snapshot.Files[i]
		}
	}
	if selected == nil {
		return nil, ErrEditorFile
	}
	selected.Content = content
	var merged []byte
	for _, file := range snapshot.Files {
		expanded := expandConfigEnvironment([]byte(file.Content))
		if err := ValidateEditorYAML(expanded); err != nil {
			return nil, fmt.Errorf("%s: %w", file.Name, err)
		}
		merged, err = deepmerge.YAML(merged, []byte(file.Content))
		if err != nil {
			return nil, err
		}
	}
	if _, err := parseAndValidateConfigBytes(merged); err != nil {
		return nil, err
	}
	if !save {
		return snapshot, nil
	}
	// Check again after validation, which may read certificates or key files.
	current, err := config.editorSnapshot()
	if err != nil {
		return nil, err
	}
	if current.Revision != revision {
		return nil, ErrEditorConflict
	}
	info, err := os.Stat(selected.path)
	if err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(selected.path), ".gatus-config-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temporary.Name())
	if err = temporary.Chmod(info.Mode().Perm()); err == nil {
		_, err = temporary.WriteString(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	// The existing watcher compares whole seconds. Ensure even an immediate
	// post-startup save gets a modification timestamp that it will detect.
	modified := time.Now()
	if !modified.After(config.lastFileModTime.Truncate(time.Second).Add(time.Second)) {
		modified = config.lastFileModTime.Truncate(time.Second).Add(time.Second)
	}
	if err := os.Chtimes(temporary.Name(), modified, modified); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary.Name(), selected.path); err != nil {
		return nil, err
	}
	return config.editorSnapshot()
}

func expandConfigEnvironment(content []byte) []byte {
	text := strings.ReplaceAll(string(content), "$$", "__GATUS_LITERAL_DOLLAR_SIGN__")
	return []byte(strings.ReplaceAll(os.ExpandEnv(text), "__GATUS_LITERAL_DOLLAR_SIGN__", "$"))
}
