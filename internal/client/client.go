// Package client habla con la API de 3D-Vault. Lo usan 3dvault-sync push/pull y,
// en la Fase 2, el agente de sincronización.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"3d-vault/internal/chunker"
	"3d-vault/internal/manifest"
	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

const (
	jsonTimeout  = 2 * time.Minute
	chunkTimeout = 5 * time.Minute
	// commitTimeout es largo porque el servidor verifica el archivo completo antes
	// de confirmar una versión nueva.
	commitTimeout = time.Hour

	maxErrorBody = 1 << 20
	maxJSONBody  = 64 << 20
)

// Config configura un Client.
type Config struct {
	Server string
	Token  string
	// ClientID identifica esta PC: los bloqueos pertenecen a una PC concreta.
	ClientID   string
	ClientName string
	// HTTP permite sustituir el cliente HTTP en tests.
	HTTP *http.Client
	// RetryDelay es la espera inicial entre reintentos. Por defecto, medio segundo.
	RetryDelay time.Duration
}

// Client es seguro para uso concurrente.
type Client struct {
	base       string
	token      string
	clientID   string
	clientName string
	http       *http.Client
	retryDelay time.Duration
}

// New devuelve un Client.
func New(cfg Config) *Client {
	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	delay := cfg.RetryDelay
	if delay == 0 {
		delay = 500 * time.Millisecond
	}
	return &Client{
		base:       strings.TrimRight(cfg.Server, "/"),
		token:      cfg.Token,
		clientID:   cfg.ClientID,
		clientName: cfg.ClientName,
		http:       httpClient,
		retryDelay: delay,
	}
}

// ServerError es una respuesta de error sin un código de negocio.
type ServerError struct {
	Status  int
	Message string
}

func (e *ServerError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "el servidor respondió " + strconv.Itoa(e.Status)
}

func (c *Client) request(ctx context.Context, timeout time.Duration, method, path string, body io.Reader, contentType string) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("preparar la petición: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.clientID != "" {
		req.Header.Set("X-Client-Id", c.clientID)
		req.Header.Set("X-Client-Name", url.QueryEscape(c.clientName))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

// doJSON envía in y decodifica out. Los errores de negocio se devuelven como
// *rules.Error, igual que en el servidor.
func (c *Client) doJSON(ctx context.Context, timeout time.Duration, method, path string, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("codificar la petición: %w", err)
		}
		body = bytes.NewReader(raw)
		contentType = "application/json"
	}
	resp, cancel, err := c.request(ctx, timeout, method, path, body, contentType)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return readError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(out); err != nil {
		return fmt.Errorf("leer la respuesta: %w", err)
	}
	return nil
}

func readError(resp *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return &ServerError{Status: resp.StatusCode}
	}
	var ruleErr rules.Error
	if json.Unmarshal(raw, &ruleErr) == nil && ruleErr.Message != "" {
		switch ruleErr.Code {
		case "unauthorized", "bad_credentials", "too_many_attempts", "internal", "chunk_too_large", "events_pruned":
			return &ServerError{Status: resp.StatusCode, Message: ruleErr.Message}
		}
		if ruleErr.Code != "" {
			return &ruleErr
		}
		return &ServerError{Status: resp.StatusCode, Message: ruleErr.Message}
	}
	return &ServerError{Status: resp.StatusCode}
}

// retryable indica si merece la pena reintentar: fallos de red y errores del
// servidor. Un rechazo por una regla seguirá igual, y un 4xx también.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var ruleErr *rules.Error
	if errors.As(err, &ruleErr) {
		return false
	}
	var serverErr *ServerError
	if errors.As(err, &serverErr) {
		return serverErr.Status >= http.StatusInternalServerError
	}
	return true
}

const maxAttempts = 5

// withRetry reintenta con espera creciente. Solo se usa con operaciones idempotentes.
func (c *Client) withRetry(ctx context.Context, op func() error) error {
	delay := c.retryDelay
	var err error
	for attempt := 1; ; attempt++ {
		err = op()
		if err == nil || attempt == maxAttempts || !retryable(ctx, err) {
			return err
		}
		select {
		case <-time.After(delay):
			delay *= 2
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Login obtiene un token a partir de usuario y contraseña. La contraseña no se
// guarda en ningún sitio.
func (c *Client) Login(ctx context.Context, username, password, tokenName string) (string, vault.User, error) {
	var resp struct {
		User  vault.User `json:"user"`
		Token string     `json:"token"`
	}
	err := c.doJSON(ctx, jsonTimeout, http.MethodPost, "/api/login", map[string]string{
		"username": username, "password": password, "token_name": tokenName,
	}, &resp)
	if err != nil {
		return "", vault.User{}, err
	}
	return resp.Token, resp.User, nil
}

// Projects lista los proyectos.
func (c *Client) Projects(ctx context.Context) ([]vault.Project, error) {
	var resp struct {
		Projects []vault.Project `json:"projects"`
	}
	if err := c.doJSON(ctx, jsonTimeout, http.MethodGet, "/api/projects", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Projects, nil
}

// CreateProject crea un proyecto.
func (c *Client) CreateProject(ctx context.Context, name, description string) (vault.Project, error) {
	var project vault.Project
	err := c.doJSON(ctx, jsonTimeout, http.MethodPost, "/api/projects", map[string]string{"name": name, "description": description}, &project)
	return project, err
}

// Snapshot devuelve el estado de un proyecto.
func (c *Client) Snapshot(ctx context.Context, projectID int64) (vault.Snapshot, error) {
	var snap vault.Snapshot
	err := c.doJSON(ctx, jsonTimeout, http.MethodGet, fmt.Sprintf("/api/projects/%d/snapshot", projectID), nil, &snap)
	return snap, err
}

// Events devuelve los eventos posteriores a since.
func (c *Client) Events(ctx context.Context, projectID, since int64) (vault.EventPage, error) {
	var page vault.EventPage
	err := c.doJSON(ctx, jsonTimeout, http.MethodGet, fmt.Sprintf("/api/projects/%d/events?since=%d", projectID, since), nil, &page)
	return page, err
}

const missingBatch = 10_000

// MissingChunks devuelve los trozos que el servidor no tiene.
func (c *Client) MissingChunks(ctx context.Context, hashes []chunker.Hash) ([]chunker.Hash, error) {
	missing := []chunker.Hash{}
	for start := 0; start < len(hashes); start += missingBatch {
		batch := hashes[start:min(start+missingBatch, len(hashes))]
		var resp struct {
			Missing []chunker.Hash `json:"missing"`
		}
		err := c.withRetry(ctx, func() error {
			return c.doJSON(ctx, jsonTimeout, http.MethodPost, "/api/chunks/missing", map[string]any{"hashes": batch}, &resp)
		})
		if err != nil {
			return nil, err
		}
		missing = append(missing, resp.Missing...)
	}
	return missing, nil
}

// PutChunk sube un trozo, con reintentos: subir dos veces el mismo no tiene efecto.
func (c *Client) PutChunk(ctx context.Context, h chunker.Hash, data []byte) error {
	return c.withRetry(ctx, func() error {
		resp, cancel, err := c.request(ctx, chunkTimeout, http.MethodPut, "/api/chunks/"+h.String(), bytes.NewReader(data), "application/octet-stream")
		if err != nil {
			return err
		}
		defer cancel()
		defer resp.Body.Close()
		if resp.StatusCode >= http.StatusBadRequest {
			return readError(resp)
		}
		return nil
	})
}

// GetChunk descarga un trozo y verifica su hash.
func (c *Client) GetChunk(ctx context.Context, h chunker.Hash) ([]byte, error) {
	var data []byte
	err := c.withRetry(ctx, func() error {
		resp, cancel, err := c.request(ctx, chunkTimeout, http.MethodGet, "/api/chunks/"+h.String(), nil, "")
		if err != nil {
			return err
		}
		defer cancel()
		defer resp.Body.Close()
		if resp.StatusCode >= http.StatusBadRequest {
			return readError(resp)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, chunker.MaxSize+1))
		if err != nil {
			return fmt.Errorf("leer trozo: %w", err)
		}
		if chunker.Hash(sha256.Sum256(raw)) != h {
			return fmt.Errorf("el trozo %s llegó dañado", h)
		}
		data = raw
		return nil
	})
	return data, err
}

// CreateAsset crea un archivo nuevo.
func (c *Client) CreateAsset(ctx context.Context, projectID int64, path string, m manifest.Manifest, comment string) (vault.CommitResult, error) {
	var result vault.CommitResult
	err := c.withRetry(ctx, func() error {
		return c.doJSON(ctx, commitTimeout, http.MethodPost, "/api/assets", map[string]any{
			"project_id": projectID, "path": path, "manifest": m, "comment": comment,
		}, &result)
	})
	return result, err
}

// CommitInput describe una versión nueva.
type CommitInput struct {
	AssetID     int64
	BaseVersion int64
	Manifest    manifest.Manifest
	Comment     string
	ReleaseLock bool
}

// CommitVersion confirma una versión. Reintentar es seguro: si la primera llegó a
// confirmarse, la respuesta indica que no hay cambios.
func (c *Client) CommitVersion(ctx context.Context, in CommitInput) (vault.CommitResult, error) {
	var result vault.CommitResult
	err := c.withRetry(ctx, func() error {
		return c.doJSON(ctx, commitTimeout, http.MethodPost, fmt.Sprintf("/api/assets/%d/versions", in.AssetID), map[string]any{
			"base_version": in.BaseVersion, "manifest": in.Manifest, "size_bytes": in.Manifest.Size(),
			"comment": in.Comment, "release_lock": in.ReleaseLock,
		}, &result)
	})
	return result, err
}

// Lock bloquea un archivo.
func (c *Client) Lock(ctx context.Context, assetID int64, takeover bool) (vault.LockResult, error) {
	var result vault.LockResult
	err := c.doJSON(ctx, jsonTimeout, http.MethodPost, fmt.Sprintf("/api/assets/%d/lock", assetID), map[string]bool{"takeover": takeover}, &result)
	return result, err
}

// Unlock libera un bloqueo.
func (c *Client) Unlock(ctx context.Context, assetID int64) error {
	return c.doJSON(ctx, jsonTimeout, http.MethodDelete, fmt.Sprintf("/api/assets/%d/lock", assetID), nil, nil)
}

// Versions devuelve el historial de un archivo.
func (c *Client) Versions(ctx context.Context, assetID int64) ([]vault.Version, error) {
	var resp struct {
		Versions []vault.Version `json:"versions"`
	}
	if err := c.doJSON(ctx, jsonTimeout, http.MethodGet, fmt.Sprintf("/api/assets/%d/versions", assetID), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Versions, nil
}

// Manifest devuelve el manifiesto de una versión.
func (c *Client) Manifest(ctx context.Context, versionID int64) (manifest.Manifest, error) {
	var m manifest.Manifest
	err := c.withRetry(ctx, func() error {
		return c.doJSON(ctx, jsonTimeout, http.MethodGet, fmt.Sprintf("/api/versions/%d/manifest", versionID), nil, &m)
	})
	return m, err
}
