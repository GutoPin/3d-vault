package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"3d-vault/internal/chunker"
	"3d-vault/internal/manifest"
	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

const (
	maxJSONBody     = 1 << 20  // 1 MiB
	maxManifestBody = 32 << 20 // Un archivo de 20 GB en trozos mínimos, con margen.
	maxClientID     = 128
	maxClientName   = 100

	jsonTimeout  = 2 * time.Minute
	chunkTimeout = 5 * time.Minute
	writeStall   = 2 * time.Minute
)

func (s *Server) apiRoutes(r chi.Router) {
	r.Route("/api", func(r chi.Router) {
		r.Post("/login", s.apiLogin)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAPIUser)

			r.Post("/logout", s.apiLogout)
			r.Get("/me", s.apiMe)
			r.Post("/tokens", s.apiCreateToken)
			r.Delete("/tokens/{id}", s.apiRevokeToken)

			r.Get("/projects", s.apiListProjects)
			r.Post("/projects", s.apiCreateProject)
			r.Get("/projects/{id}/snapshot", s.apiSnapshot)
			r.Post("/projects/{id}/folders", s.apiCreateFolder)
			r.Delete("/projects/{id}/folders", s.apiDeleteFolder)
			r.Post("/projects/{id}/folders/rename", s.apiRenameFolder)
			r.Get("/projects/{id}/folders/info", s.apiFolderInfo)
			r.Get("/projects/{id}/events", s.apiEvents)
			r.Get("/stream", s.apiStream)

			r.Post("/chunks/missing", s.apiMissingChunks)
			r.Put("/chunks/{sha256}", s.apiPutChunk)
			r.Get("/chunks/{sha256}", s.apiGetChunk)

			r.Post("/assets", s.apiCreateAsset)
			r.Get("/assets/{id}/versions", s.apiListVersions)
			r.Get("/assets/{id}/links", s.apiLinks)
			r.Post("/assets/{id}/versions", s.apiCommitVersion)
			r.Post("/assets/{id}/lock", s.apiLock)
			r.Delete("/assets/{id}/lock", s.apiUnlock)
			r.Post("/assets/{id}/request-unlock", s.apiRequestUnlock)
			r.Post("/assets/{id}/restore/{n}", s.apiRestore)
			r.Post("/assets/{id}/move", s.apiMove)
			r.Delete("/assets/{id}", s.apiDelete)

			r.Get("/versions/{id}/manifest", s.apiManifest)
			r.Get("/versions/{id}/download", s.apiDownload)
		})
	})
}

func (s *Server) requireAPIUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := currentUser(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "unauthorized", "message": "Inicia sesión para continuar."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// La respuesta ya empezó: si falla aquí, el cliente lo ve como un corte.
	_ = json.NewEncoder(w).Encode(body)
}

// writeError responde con un rechazo de negocio tal cual. Cualquier otro error se
// registra y se oculta al cliente.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ruleErr *rules.Error
	if errors.As(err, &ruleErr) {
		writeJSON(w, statusForCode(ruleErr.Code), ruleErr)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	level := slog.LevelError
	message := "Error interno."
	if isCorruption(err) {
		message = "Hay un problema con los datos guardados. Avisa al administrador."
	}
	s.log.Log(r.Context(), level, "error en la petición", slog.String("ruta", r.URL.Path), slog.Any("error", err))
	writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "internal", "message": message})
}

// decodeJSON lee un cuerpo JSON acotado. Rechaza campos desconocidos para detectar
// clientes desalineados con la API.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &rules.Error{Code: rules.CodeInvalidInput, Message: "Cuerpo de la petición no válido."}
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		return 0, &rules.Error{Code: rules.CodeNotFound, Message: "El recurso no existe."}
	}
	return id, nil
}

func pathHash(r *http.Request) (chunker.Hash, error) {
	h, err := chunker.ParseHash(chi.URLParam(r, "sha256"))
	if err != nil {
		return chunker.Hash{}, &rules.Error{Code: rules.CodeInvalidInput, Message: "Hash no válido."}
	}
	return h, nil
}

// clientActor construye el actor de una escritura. Un bloqueo pertenece a una PC,
// así que los clientes con token deben identificarla. La web usa siempre "web".
func clientActor(r *http.Request) (rules.Actor, error) {
	user, ok := currentUser(r)
	if !ok {
		return rules.Actor{}, errors.New("petición sin usuario")
	}
	actor := rules.Actor{UserID: user.ID, IsAdmin: user.IsAdmin}

	clientID := r.Header.Get("X-Client-Id")
	if clientID == "" {
		method, _ := r.Context().Value(authMethodKey).(authMethod)
		if method != authSession {
			return rules.Actor{}, &rules.Error{Code: rules.CodeInvalidInput, Message: "Falta la cabecera X-Client-Id."}
		}
		actor.ClientID, actor.ClientName = webClientID, webClientName
		return actor, nil
	}
	if len(clientID) > maxClientID {
		return rules.Actor{}, &rules.Error{Code: rules.CodeInvalidInput, Message: "X-Client-Id es demasiado largo."}
	}
	actor.ClientID = clientID

	// Va escapado porque el nombre de una PC puede tener acentos.
	name, err := url.QueryUnescape(r.Header.Get("X-Client-Name"))
	if err != nil || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxClientName {
		return rules.Actor{}, &rules.Error{Code: rules.CodeInvalidInput, Message: "X-Client-Name no es válido."}
	}
	actor.ClientName = strings.TrimSpace(name)
	if actor.ClientName == "" {
		actor.ClientName = clientID
	}
	return actor, nil
}

// extendDeadlines fija plazos por petición. El servidor no tiene plazos globales
// porque SSE, las descargas y la verificación de un archivo pueden durar mucho.
func extendDeadlines(w http.ResponseWriter, read, write time.Duration) {
	rc := http.NewResponseController(w)
	now := time.Now()
	if read > 0 {
		_ = rc.SetReadDeadline(now.Add(read))
	}
	if write > 0 {
		_ = rc.SetWriteDeadline(now.Add(write))
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// TokenName pide un token de API en lugar de una cookie de sesión. Es lo que
	// usa el agente, que no guarda la contraseña.
	TokenName string `json:"token_name,omitempty"`
}

type loginResponse struct {
	User    vault.User `json:"user"`
	Token   string     `json:"token,omitempty"`
	TokenID int64      `json:"token_id,omitempty"`
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	var req loginRequest
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	user, err := s.login(r, req.Username, req.Password)
	switch {
	case errors.Is(err, errTooManyAttempts):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"code": "too_many_attempts", "message": err.Error()})
		return
	case errors.Is(err, errBadCredentials):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "bad_credentials", "message": err.Error()})
		return
	case err != nil:
		s.writeError(w, r, err)
		return
	}

	resp := loginResponse{User: user}
	if req.TokenName != "" {
		token, plain, err := s.svc.CreateToken(r.Context(), user.ID, req.TokenName)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		resp.Token, resp.TokenID = plain, token.ID
	} else {
		s.setSessionCookie(w, user)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) apiLogout(w http.ResponseWriter, _ *http.Request) {
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r)
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) apiCreateToken(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	user, _ := currentUser(r)
	token, plain, err := s.svc.CreateToken(r.Context(), user.ID, req.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		vault.Token
		Value string `json:"token"`
	}{token, plain})
}

func (s *Server) apiRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	user, _ := currentUser(r)
	if err := s.svc.RevokeToken(r.Context(), rules.Actor{UserID: user.ID, IsAdmin: user.IsAdmin}, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.svc.ListProjects(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) apiCreateProject(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	user, _ := currentUser(r)
	project, err := s.svc.CreateProject(r.Context(), rules.Actor{UserID: user.ID}, req.Name, req.Description)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) apiSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	snap, err := s.svc.GetSnapshot(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if _, err := s.svc.GetProject(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.svc.EventsSince(r.Context(), []int64{id}, since)
	if errors.Is(err, vault.ErrEventsPruned) {
		writeJSON(w, http.StatusGone, map[string]string{"code": "events_pruned", "message": "Esos eventos ya no están disponibles: pide un estado completo."})
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func parseSince(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	since, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || since < 0 {
		return 0, &rules.Error{Code: rules.CodeInvalidInput, Message: "El parámetro since no es válido."}
	}
	return since, nil
}

func parseProjectIDs(raw string) ([]int64, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, &rules.Error{Code: rules.CodeInvalidInput, Message: "El parámetro projects no es válido."}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// apiStream envía eventos por SSE. Consulta siempre la tabla de eventos y usa el
// Hub solo para despertar, así que un aviso perdido no pierde cambios.
func (s *Server) apiStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ids, err := parseProjectIDs(r.URL.Query().Get("projects"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Last-Event-ID lo envía el navegador al reconectar y tiene prioridad.
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("since")
	}
	var since int64
	if cursor == "" {
		// Sin punto de partida, solo interesa lo que ocurra desde ahora.
		since, err = s.svc.CurrentSeq(ctx)
	} else {
		since, err = parseSince(cursor)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")

	heartbeat := time.NewTicker(s.heartbeat)
	defer heartbeat.Stop()
	deadline := time.NewTimer(s.maxStreamAge)
	defer deadline.Stop()

	flush := func() bool {
		return rc.Flush() == nil
	}
	send := func(name string, id int64, data any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("codificar evento: %w", err)
		}
		_ = rc.SetWriteDeadline(time.Now().Add(writeStall))
		if id > 0 {
			if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
				return err
			}
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
		return err
	}

	greeted := false
	for {
		// Pedir el canal antes de consultar evita perder un aviso entre medias.
		wake := s.svc.Hub().Wait()

		page, err := s.svc.EventsSince(ctx, ids, since)
		if errors.Is(err, vault.ErrEventsPruned) {
			if send("reset", 0, map[string]string{"message": "Pide un estado completo."}) == nil {
				flush()
			}
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				s.log.ErrorContext(ctx, "error en el flujo de eventos", slog.Any("error", err))
			}
			return
		}

		if !greeted {
			if send("hello", 0, map[string]any{"seq": since, "server_epoch": page.ServerEpoch}) != nil {
				return
			}
			greeted = true
		}
		for _, event := range page.Events {
			if send(event.Kind, event.Seq, event) != nil {
				return
			}
		}
		since = page.Seq
		if !flush() {
			return
		}
		if len(page.Events) > 0 {
			continue
		}

		select {
		case <-wake:
		case <-heartbeat.C:
			_ = rc.SetWriteDeadline(time.Now().Add(writeStall))
			if _, err := io.WriteString(w, ": latido\n\n"); err != nil || !flush() {
				return
			}
		case <-deadline.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) apiMissingChunks(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	var req struct {
		Hashes []chunker.Hash `json:"hashes"`
	}
	if err := decodeJSON(w, r, maxManifestBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	missing, err := s.svc.MissingChunks(r.Context(), req.Hashes)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"missing": missing})
}

func (s *Server) apiPutChunk(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, chunkTimeout, chunkTimeout)
	h, err := pathHash(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, chunker.MaxSize))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"code": "chunk_too_large", "message": "El trozo supera el tamaño máximo."})
			return
		}
		// El cliente cortó la conexión a medias: no se guarda nada.
		return
	}
	if err := s.svc.PutChunk(r.Context(), h, data); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiGetChunk(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, chunkTimeout)
	h, err := pathHash(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	data, err := s.svc.GetChunk(r.Context(), h)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	headers := w.Header()
	headers.Set("Content-Type", "application/octet-stream")
	headers.Set("Content-Length", strconv.Itoa(len(data)))
	// Un trozo se identifica por su contenido: nunca cambia.
	headers.Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

type createAssetRequest struct {
	ProjectID int64             `json:"project_id"`
	Path      string            `json:"path"`
	Manifest  manifest.Manifest `json:"manifest"`
	Comment   string            `json:"comment"`
}

// commitStatus indica en el código HTTP si se creó algo.
func commitStatus(result vault.CommitResult) int {
	if result.NoChanges {
		return http.StatusOK
	}
	return http.StatusCreated
}

func (s *Server) apiCreateAsset(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, chunkTimeout, 0)
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req createAssetRequest
	if err := decodeJSON(w, r, maxManifestBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.svc.CreateAsset(r.Context(), actor, vault.CreateAssetInput{
		ProjectID: req.ProjectID,
		Path:      req.Path,
		Manifest:  req.Manifest,
		Comment:   req.Comment,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, commitStatus(result), result)
}

func (s *Server) apiListVersions(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	versions, err := s.svc.ListVersions(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

type commitRequest struct {
	BaseVersion int64             `json:"base_version"`
	Manifest    manifest.Manifest `json:"manifest"`
	SizeBytes   int64             `json:"size_bytes"`
	Comment     string            `json:"comment"`
	ReleaseLock bool              `json:"release_lock"`
}

func (s *Server) apiCommitVersion(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, chunkTimeout, 0)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req commitRequest
	if err := decodeJSON(w, r, maxManifestBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.svc.CommitVersion(r.Context(), actor, vault.CommitInput{
		AssetID:     id,
		BaseVersion: req.BaseVersion,
		Manifest:    req.Manifest,
		SizeBytes:   req.SizeBytes,
		Comment:     req.Comment,
		ReleaseLock: req.ReleaseLock,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, commitStatus(result), result)
}

func (s *Server) apiLock(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		Takeover bool `json:"takeover"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	result, err := s.svc.Lock(r.Context(), actor, id, req.Takeover)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) apiUnlock(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := s.svc.Unlock(r.Context(), actor, id, force); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiRestore(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	number, err := pathID(r, "n")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		BaseVersion int64 `json:"base_version"`
	}
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.svc.Restore(r.Context(), actor, id, number, req.BaseVersion)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, commitStatus(result), result)
}

func (s *Server) apiMove(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		BaseVersion int64  `json:"base_version"`
		Path        string `json:"path"`
	}
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	asset, err := s.svc.Move(r.Context(), actor, id, req.BaseVersion, req.Path)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

// apiDelete recibe base_version por la URL: un cuerpo en DELETE no es fiable a
// través de proxies.
func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	base, err := parseSince(r.URL.Query().Get("base_version"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.Delete(r.Context(), actor, id, base); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiManifest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	file, err := s.svc.GetVersionFile(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, file.Manifest)
}

// apiDownload reconstruye una versión en streaming.
func (s *Server) apiDownload(w http.ResponseWriter, r *http.Request) {
	s.streamVersion(w, r)
}

func (s *Server) streamVersion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	file, err := s.svc.GetVersionFile(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.sendVersion(w, r, file, "application/octet-stream", "attachment")
}

// sendVersion reconstruye la versión en streaming. contentType y disposition los
// elige el servidor, nunca quien hace la petición.
func (s *Server) sendVersion(w http.ResponseWriter, r *http.Request, file vault.VersionFile, contentType, disposition string) {
	id := file.Version.ID
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.FormatInt(file.Version.SizeBytes, 10))
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(file.Path)}))
	h.Set("ETag", `"`+file.Version.FileSHA256+`"`)

	rc := http.NewResponseController(w)
	for _, c := range file.Manifest.Chunks {
		data, err := s.svc.ReadChunkData(c.SHA256)
		if err != nil {
			// Las cabeceras ya se enviaron. Cortar la conexión hace que el cliente
			// note el error, en lugar de guardar un archivo incompleto como bueno.
			s.log.ErrorContext(r.Context(), "error al reconstruir una versión", slog.Int64("versión", id), slog.Any("error", err))
			panic(http.ErrAbortHandler)
		}
		// El plazo se renueva por trozo: una descarga larga es válida, una parada no.
		_ = rc.SetWriteDeadline(time.Now().Add(writeStall))
		if _, err := w.Write(data); err != nil {
			return
		}
	}
}
