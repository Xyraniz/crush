package vtuber

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateModelPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	validPath := filepath.Join(dir, "avatar.vrm")
	require.NoError(t, os.WriteFile(validPath, []byte("glTF\x02\x00\x00\x00"), 0o600))

	resolved, err := validateModelPath(validPath)
	require.NoError(t, err)
	require.Equal(t, validPath, resolved)

	_, err = validateModelPath(filepath.Join(dir, "avatar.glb"))
	require.ErrorContains(t, err, ".vrm")

	badPath := filepath.Join(dir, "bad.vrm")
	require.NoError(t, os.WriteFile(badPath, []byte("nope"), 0o600))
	_, err = validateModelPath(badPath)
	require.ErrorContains(t, err, "binary glTF/VRM")
}

func TestViewerRoutesOnlyServeTheSelectedModel(t *testing.T) {
	t.Parallel()

	modelPath := filepath.Join(t.TempDir(), "avatar.vrm")
	modelBytes := []byte("glTF test model")
	require.NoError(t, os.WriteFile(modelPath, modelBytes, 0o600))
	model := &modelState{path: modelPath, revision: 1}
	server := httptest.NewServer(newHandler(model, "secret"))
	defer server.Close()

	response, err := http.Get(server.URL + "/secret")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, response.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
	require.NoError(t, response.Body.Close())

	response, err = http.Get(server.URL + "/secret/version")
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.JSONEq(t, `{"revision":1,"enabled":true}`, string(body))

	response, err = http.Get(server.URL + "/secret/model.vrm")
	require.NoError(t, err)
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	body, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, modelBytes, body)

	response, err = http.Get(server.URL + "/secret/unexpected")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.NoError(t, response.Body.Close())
}

func TestViewerSetModelPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := filepath.Join(dir, "first.vrm")
	second := filepath.Join(dir, "second.vrm")
	require.NoError(t, os.WriteFile(first, []byte("glTF first"), 0o600))
	require.NoError(t, os.WriteFile(second, []byte("glTF second"), 0o600))
	viewer := &Viewer{model: &modelState{path: first, revision: 1}}

	require.NoError(t, viewer.SetModelPath(second))
	viewer.model.RLock()
	require.Equal(t, second, viewer.model.path)
	require.EqualValues(t, 2, viewer.model.revision)
	viewer.model.RUnlock()

	require.NoError(t, viewer.SetModelPath(""))
	viewer.model.RLock()
	require.Empty(t, viewer.model.path)
	require.EqualValues(t, 3, viewer.model.revision)
	viewer.model.RUnlock()
}

func TestViewerUsesTransparentBackgroundAndLoweredArmPose(t *testing.T) {
	t.Parallel()

	require.Contains(t, viewerHTML, "background: transparent;")
	require.Contains(t, viewerHTML, "leftArm.rotation.z = 0.95;")
	require.Contains(t, viewerHTML, "rightArm.rotation.z = -0.95;")
}
