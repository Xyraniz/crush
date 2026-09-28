package vtuber

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed viewer.html
var viewerHTML string

// Viewer serves one local VRM to its desktop overlay.
type Viewer struct {
	server       *http.Server
	model        *modelState
	closeOverlay func()
}

type modelState struct {
	sync.RWMutex
	path     string
	revision uint64
}

// Start opens a desktop window that renders modelPath as an animated avatar.
func Start(modelPath string) (*Viewer, error) {
	modelPath, err := validateModelPath(modelPath)
	if err != nil {
		return nil, err
	}

	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, fmt.Errorf("create avatar viewer token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes[:])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start avatar viewer: %w", err)
	}

	model := &modelState{path: modelPath, revision: 1}
	server := &http.Server{Handler: newHandler(model, token)}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Avatar viewer server failed", "error", err)
		}
	}()

	url := "http://" + listener.Addr().String() + "/" + token
	closeOverlay, err := openOverlay(url)
	if err != nil {
		_ = server.Close()
		return nil, fmt.Errorf("open avatar overlay: %w", err)
	}
	return &Viewer{server: server, model: model, closeOverlay: closeOverlay}, nil
}

// SetModelPath changes the avatar shown by the already-open viewer. An empty
// path disables the avatar without closing the overlay window.
func (v *Viewer) SetModelPath(path string) error {
	if path != "" {
		resolved, err := validateModelPath(path)
		if err != nil {
			return err
		}
		path = resolved
	}

	v.model.Lock()
	v.model.path = path
	v.model.revision++
	v.model.Unlock()
	return nil
}

// Close stops the overlay and its local model server.
func (v *Viewer) Close() {
	if v.closeOverlay != nil {
		v.closeOverlay()
	}
	_ = v.server.Close()
}

func validateModelPath(path string) (string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".vrm") {
		return "", fmt.Errorf("avatar must be a .vrm file")
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read avatar: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("avatar must be a regular file")
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open avatar: %w", err)
	}
	defer file.Close()

	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return "", fmt.Errorf("read avatar header: %w", err)
	}
	if string(magic[:]) != "glTF" {
		return "", fmt.Errorf("avatar is not a binary glTF/VRM file")
	}

	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve avatar path: %w", err)
	}
	return path, nil
}

func newHandler(model *modelState, token string) http.Handler {
	viewerPath := "/" + token
	modelURL := viewerPath + "/model.vrm"
	versionURL := viewerPath + "/version"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; connect-src 'self' blob:; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		switch r.URL.Path {
		case viewerPath:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, viewerHTML)
		case modelURL:
			model.RLock()
			path := model.path
			model.RUnlock()
			if path == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, path)
		case versionURL:
			model.RLock()
			revision, enabled := model.revision, model.path != ""
			model.RUnlock()
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = fmt.Fprintf(w, `{"revision":%d,"enabled":%t}`, revision, enabled)
		default:
			http.NotFound(w, r)
		}
	})
}
