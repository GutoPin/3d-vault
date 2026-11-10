package server

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

const (
	// renderSendTimeout acota un envío: 20 GB a 10 MB/s tardan media hora.
	renderSendTimeout = 6 * time.Hour
	// renderSendKeep es cuánto se recuerda un envío terminado.
	renderSendKeep = time.Hour
	// maxRenderSends limita los envíos a la vez: cada uno lee y sube GB.
	maxRenderSends = 2
)

// RenderConfig conecta con la página de render. Sin URL, no hay botón.
type RenderConfig struct {
	URL string
	Key string
}

type renderSend struct {
	id      string
	assetID int64
	userID  int64
	total   int64
	done    atomic.Int64

	mu      sync.Mutex
	state   string // enviando, listo, error
	message string
	url     string
	ended   time.Time
}

func (r *renderSend) finish(state, message, target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state, r.message, r.url, r.ended = state, message, target, time.Now()
}

// renderSends guarda los envíos en memoria: si el servidor se reinicia, se
// vuelve a pulsar el botón.
type renderSends struct {
	mu   sync.Mutex
	byID map[string]*renderSend
}

// start registra un envío. Si esa escena ya se está enviando, devuelve ese.
func (rs *renderSends) start(assetID, userID, total int64) (send *renderSend, isNew bool, err error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.byID == nil {
		rs.byID = map[string]*renderSend{}
	}
	active := 0
	for id, r := range rs.byID {
		r.mu.Lock()
		state, ended := r.state, r.ended
		r.mu.Unlock()
		if state != "enviando" {
			if time.Since(ended) > renderSendKeep {
				delete(rs.byID, id)
			}
			continue
		}
		if r.assetID == assetID {
			return r, false, nil
		}
		active++
	}
	if active >= maxRenderSends {
		return nil, false, &rules.Error{Code: rules.CodeInvalidInput, Message: "Ya se están enviando otras escenas a render. Inténtalo en unos minutos."}
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, false, err
	}
	send = &renderSend{id: hex.EncodeToString(raw), assetID: assetID, userID: userID, total: total, state: "enviando"}
	rs.byID[send.id] = send
	return send, true, nil
}

func (rs *renderSends) get(id string) *renderSend {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.byID[id]
}

// apiRenderPlan dice qué se mandaría a renderizar y qué falta.
func (s *Server) apiRenderPlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	plan, err := s.svc.RenderPlan(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// apiRenderSend empieza a mandar la escena y todo lo que usa a la página de
// render. Solo la deja allí: nada se alquila hasta que alguien lo confirme.
func (s *Server) apiRenderSend(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	if s.renderCfg.URL == "" {
		s.writeError(w, r, &rules.Error{Code: rules.CodeInvalidInput, Message: "La conexión con la página de render no está configurada."})
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	plan, err := s.svc.RenderPlan(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if plan.Pending {
		s.writeError(w, r, &rules.Error{Code: rules.CodeInvalidInput, Message: "Todavía se está revisando qué archivos usa esta escena. Inténtalo en un momento."})
		return
	}
	if len(plan.Blocking) > 0 {
		s.writeError(w, r, &rules.Error{Code: rules.CodeInvalidInput, Message: "Faltan archivos enlazados en 3D-Vault: sin ellos, esos objetos no saldrían en el render."})
		return
	}
	user, _ := currentUser(r)
	send, isNew, err := s.renderSends.start(id, user.ID, plan.Bytes)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if isNew {
		go s.runRenderSend(send, plan)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": send.id})
}

// apiRenderStatus dice cómo va un envío. Solo lo ve quien lo empezó.
func (s *Server) apiRenderStatus(w http.ResponseWriter, r *http.Request) {
	send := s.renderSends.get(chi.URLParam(r, "send"))
	user, _ := currentUser(r)
	if send == nil || (send.userID != user.ID && !user.IsAdmin) {
		s.writeError(w, r, &rules.Error{Code: rules.CodeNotFound, Message: "Ese envío no existe."})
		return
	}
	send.mu.Lock()
	defer send.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"state": send.state, "message": send.message, "url": send.url, "done": send.done.Load(), "total": send.total,
	})
}

func (s *Server) runRenderSend(send *renderSend, plan vault.RenderPlan) {
	ctx, cancel := context.WithTimeout(context.Background(), renderSendTimeout)
	defer cancel()
	target, err := s.uploadRender(ctx, send, plan)
	if err != nil {
		s.log.ErrorContext(ctx, "error al enviar a render", slog.Int64("archivo", send.assetID), slog.Any("error", err))
		var re *rules.Error
		message := "No se pudo enviar a la página de render. Inténtalo de nuevo."
		if errors.As(err, &re) {
			message = re.Message
		}
		send.finish("error", message, "")
		return
	}
	send.finish("listo", "", target)
}

// uploadRender arma el .zip mientras lo sube: no ocupa disco ni memoria aquí.
func (s *Server) uploadRender(ctx context.Context, send *renderSend, plan vault.RenderPlan) (string, error) {
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() {
		writer.CloseWithError(s.writeRenderZip(ctx, writer, send, plan))
	}()

	name := strings.TrimSuffix(path.Base(plan.Scene), path.Ext(plan.Scene)) + " con enlaces.zip"
	query := url.Values{"nombre": {name}, "escena": {plan.Scene}}
	base := strings.TrimRight(s.renderCfg.URL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/puente/subida?"+query.Encode(), reader)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/zip")
	req.Header.Set("X-Puente-Clave", s.renderCfg.Key)
	req.Header.Set("X-Tamano", strconv.FormatInt(plan.Bytes, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("subir: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Token  string `json:"token"`
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = json.Unmarshal(raw, &body)
	if resp.StatusCode != http.StatusOK || body.Token == "" {
		if message := body.Error + body.Detail; message != "" && resp.StatusCode == http.StatusBadRequest {
			return "", &rules.Error{Code: rules.CodeInvalidInput, Message: message}
		}
		return "", fmt.Errorf("la página de render respondió %d: %s", resp.StatusCode, raw)
	}
	return base + "/#renderizar/" + url.PathEscape(body.Token), nil
}

func (s *Server) writeRenderZip(ctx context.Context, w io.Writer, send *renderSend, plan vault.RenderPlan) error {
	zw := zip.NewWriter(w)
	for _, f := range plan.Files {
		file, err := s.svc.GetVersionFile(ctx, f.VersionID)
		if err != nil {
			return err
		}
		entry, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Store, Modified: file.Version.CreatedAt.Local()})
		if err != nil {
			return err
		}
		for _, c := range file.Manifest.Chunks {
			data, err := s.svc.ReadChunkData(c.SHA256)
			if err != nil {
				return err
			}
			if _, err := entry.Write(data); err != nil {
				return err
			}
			send.done.Add(int64(len(data)))
		}
	}
	return zw.Close()
}
