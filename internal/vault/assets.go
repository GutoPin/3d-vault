package vault

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"3d-vault/internal/chunker"
	"3d-vault/internal/manifest"
	"3d-vault/internal/rules"
	"3d-vault/internal/store"
)

// CreateAssetInput describe un archivo nuevo.
type CreateAssetInput struct {
	ProjectID int64
	Path      string
	Manifest  manifest.Manifest
	Comment   string
}

// CommitInput describe una versión nueva de un archivo existente.
type CommitInput struct {
	AssetID     int64
	BaseVersion int64
	Manifest    manifest.Manifest
	// SizeBytes es el tamaño que el cliente cree haber subido. Se contrasta con el
	// manifiesto para detectar errores en el cliente.
	SizeBytes   int64
	Comment     string
	ReleaseLock bool
}

// CommitResult es el resultado de crear o confirmar una versión.
type CommitResult struct {
	Asset   Asset   `json:"asset"`
	Version Version `json:"version"`
	// NoChanges indica que el contenido ya era la última versión y no se creó otra.
	NoChanges bool `json:"no_changes"`
}

// ensureManifest registra un manifiesto y devuelve su id. Si es nuevo, primero
// reconstruye el archivo y verifica su hash. Va fuera de la transacción de la
// versión: un archivo de 20 GB tardaría minutos y bloquearía a todo el servidor.
// Un manifiesto verificado sin versión que lo use es inofensivo.
func (s *Service) ensureManifest(ctx context.Context, m manifest.Manifest) (int64, error) {
	var (
		id      int64
		missing []chunker.Hash
	)
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM manifests WHERE file_sha256 = ?`, m.FileSHA256.String()).Scan(&id)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("consultar manifiesto: %w", err)
		}
		hashes := make([]chunker.Hash, len(m.Chunks))
		for i, c := range m.Chunks {
			hashes[i] = c.SHA256
		}
		missing, err = missingChunks(ctx, tx, hashes)
		return err
	})
	if err != nil {
		return 0, err
	}
	if id != 0 {
		return id, nil
	}
	if len(missing) > 0 {
		names := make([]string, len(missing))
		for i, h := range missing {
			names[i] = h.String()
		}
		return 0, &rules.Error{
			Code:    rules.CodeMissingChunks,
			Message: fmt.Sprintf("Faltan %d trozos por subir.", len(missing)),
			Missing: names,
		}
	}

	if err := s.verifyManifest(ctx, m); err != nil {
		return 0, err
	}

	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO manifests(file_sha256, chunk_count, size_bytes) VALUES (?, ?, ?)`,
			m.FileSHA256.String(), len(m.Chunks), m.Size())
		if err != nil {
			return fmt.Errorf("crear manifiesto: %w", err)
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("crear manifiesto: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM manifests WHERE file_sha256 = ?`, m.FileSHA256.String()).Scan(&id); err != nil {
			return fmt.Errorf("leer manifiesto: %w", err)
		}
		if inserted == 0 {
			// Otra subida idéntica lo registró mientras se verificaba este.
			return nil
		}

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO manifest_chunks(manifest_id, idx, chunk_sha256, size) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("preparar manifiesto: %w", err)
		}
		defer stmt.Close()
		for i, c := range m.Chunks {
			if _, err := stmt.ExecContext(ctx, id, i, c.SHA256.String(), c.Size); err != nil {
				return fmt.Errorf("guardar trozo %d del manifiesto: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// verifyManifest reconstruye el archivo en orden y compara su hash. Sin esto, un
// cliente con un error dejaría un manifiesto incorrecto que file_sha256 UNIQUE
// reutilizaría para siempre.
func (s *Service) verifyManifest(ctx context.Context, m manifest.Manifest) error {
	fileHash := sha256.New()
	for i, c := range m.Chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := s.store.Get(c.SHA256)
		if err != nil {
			return fmt.Errorf("leer trozo %d: %w", i, err)
		}
		if int64(len(data)) != c.Size {
			return &rules.Error{Code: rules.CodeManifestMismatch, Message: fmt.Sprintf("El trozo %d no mide lo que indica el manifiesto.", i)}
		}
		fileHash.Write(data)
	}
	if chunker.Hash(fileHash.Sum(nil)) != m.FileSHA256 {
		return &rules.Error{Code: rules.CodeManifestMismatch, Message: "Los trozos no reconstruyen el archivo indicado."}
	}
	return nil
}

func (s *Service) validateManifest(m manifest.Manifest) error {
	if err := m.Validate(s.maxFileSize); err != nil {
		return &rules.Error{Code: rules.CodeInvalidManifest, Message: "Manifiesto no válido: " + err.Error()}
	}
	return nil
}

// newVersion describe la versión que va a crear insertVersion.
type newVersion struct {
	asset        Asset
	manifestID   int64
	fileSHA256   string
	sizeBytes    int64
	comment      string
	restoredFrom *int64
	action       string
	eventKind    string
}

// insertVersion crea la versión siguiente. Debe llamarse dentro de una transacción
// de escritura que ya haya validado las reglas.
func (s *Service) insertVersion(ctx context.Context, tx *sql.Tx, actor rules.Actor, v newVersion) (Version, error) {
	number := v.asset.HeadVersion + 1
	now := s.now()

	var restored any
	if v.restoredFrom != nil {
		restored = *v.restoredFrom
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO versions(asset_id, number, file_sha256, size_bytes, manifest_id, comment, uploaded_by, created_at, restored_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.asset.ID, number, v.fileSHA256, v.sizeBytes, v.manifestID, v.comment, actor.UserID, millis(now), restored)
	if err != nil {
		return Version{}, fmt.Errorf("crear versión: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Version{}, fmt.Errorf("leer id de la versión: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assets SET head_version = ? WHERE id = ?`, number, v.asset.ID); err != nil {
		return Version{}, fmt.Errorf("actualizar archivo: %w", err)
	}

	var actorName string
	if err := tx.QueryRowContext(ctx, `SELECT display_name FROM users WHERE id = ?`, actor.UserID).Scan(&actorName); err != nil {
		return Version{}, fmt.Errorf("leer usuario: %w", err)
	}

	version := Version{
		ID:           id,
		AssetID:      v.asset.ID,
		Number:       number,
		FileSHA256:   v.fileSHA256,
		SizeBytes:    v.sizeBytes,
		Comment:      v.comment,
		UploadedByID: actor.UserID,
		UploadedBy:   actorName,
		CreatedAt:    fromMillis(millis(now)),
		RestoredFrom: v.restoredFrom,
		manifestID:   v.manifestID,
	}

	if err := s.addActivity(ctx, tx, activityEntry{
		userID:    actor.UserID,
		action:    v.action,
		projectID: v.asset.ProjectID,
		assetID:   v.asset.ID,
		versionID: id,
		detail:    fmt.Sprintf("v%d: %s", number, v.comment),
	}); err != nil {
		return Version{}, err
	}
	err = s.addEvent(ctx, tx, v.asset.ProjectID, v.eventKind, v.asset.ID, eventPayload{
		Path:       v.asset.Path,
		Version:    number,
		VersionID:  id,
		FileSHA256: v.fileSHA256,
		SizeBytes:  v.sizeBytes,
		Comment:    v.comment,
		ActorName:  actorName,
	})
	if err != nil {
		return Version{}, err
	}
	return version, nil
}

// CreateAsset crea un archivo con su primera versión. Si ya existe un archivo en
// esa ruta con el mismo contenido, no hace nada: así, reintentar tras un corte es
// seguro.
func (s *Service) CreateAsset(ctx context.Context, actor rules.Actor, in CreateAssetInput) (CommitResult, error) {
	comment, err := rules.CheckComment(in.Comment)
	if err != nil {
		return CommitResult{}, err
	}
	path, err := rules.NormalizePath(in.Path)
	if err != nil {
		return CommitResult{}, err
	}
	if err := s.validateManifest(in.Manifest); err != nil {
		return CommitResult{}, err
	}

	// Comprobación previa: evita verificar todo el archivo para rechazarlo después.
	var result CommitResult
	done := false
	err = s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		if err := checkProjectWritable(ctx, tx, in.ProjectID); err != nil {
			return err
		}
		var err error
		done, result, err = existingAtPath(ctx, tx, in.ProjectID, path, in.Manifest.FileSHA256.String())
		return err
	})
	if err != nil {
		return CommitResult{}, err
	}
	if done {
		return result, nil
	}

	manifestID, err := s.ensureManifest(ctx, in.Manifest)
	if err != nil {
		return CommitResult{}, err
	}

	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := checkProjectWritable(ctx, tx, in.ProjectID); err != nil {
			return err
		}
		var err error
		done, result, err = existingAtPath(ctx, tx, in.ProjectID, path, in.Manifest.FileSHA256.String())
		if err != nil || done {
			return err
		}
		if err := checkFolderFree(ctx, tx, in.ProjectID, path); err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `
			INSERT INTO assets(project_id, path, path_key, ext, head_version, created_by, created_at)
			VALUES (?, ?, ?, ?, 0, ?, ?)`,
			in.ProjectID, path.Clean, path.Key, path.Ext, actor.UserID, millis(s.now()))
		if err != nil {
			return fmt.Errorf("crear archivo: %w", err)
		}
		assetID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("leer id del archivo: %w", err)
		}

		version, err := s.insertVersion(ctx, tx, actor, newVersion{
			asset:      Asset{ID: assetID, ProjectID: in.ProjectID, Path: path.Clean},
			manifestID: manifestID,
			fileSHA256: in.Manifest.FileSHA256.String(),
			sizeBytes:  in.Manifest.Size(),
			comment:    comment,
			action:     actionCreateAsset,
			eventKind:  EventAssetCreated,
		})
		if err != nil {
			return err
		}
		asset, err := loadAsset(ctx, tx, assetID)
		if err != nil {
			return err
		}
		result = CommitResult{Asset: asset, Version: version}
		return nil
	})
	if err != nil {
		return CommitResult{}, err
	}
	if !result.NoChanges {
		s.hub.Notify()
		s.thumbnailLater(result.Asset, result.Version)
		s.refsLater(result.Asset, result.Version)
	}
	return result, nil
}

func checkProjectWritable(ctx context.Context, tx *sql.Tx, projectID int64) error {
	p, err := loadProject(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if p.Archived {
		return &rules.Error{Code: rules.CodeProjectArchived, Message: fmt.Sprintf("El proyecto «%s» está archivado.", p.Name)}
	}
	return nil
}

// existingAtPath busca un archivo activo en esa ruta. Devuelve done=true si tiene
// el mismo contenido, y un error si tiene otro.
func existingAtPath(ctx context.Context, tx *sql.Tx, projectID int64, path rules.Path, sha string) (bool, CommitResult, error) {
	asset, err := scanAsset(tx.QueryRowContext(ctx, `SELECT `+assetColumns+assetFrom+` AND a.project_id = ? AND a.path_key = ?`, projectID, path.Key))
	if errors.Is(err, sql.ErrNoRows) {
		return false, CommitResult{}, nil
	}
	if err != nil {
		return false, CommitResult{}, fmt.Errorf("consultar ruta: %w", err)
	}
	if !rules.SameContent(asset.FileSHA256, sha) {
		return false, CommitResult{}, pathExists(asset.Path)
	}
	version, err := loadVersionByNumber(ctx, tx, asset.ID, asset.HeadVersion)
	if err != nil {
		return false, CommitResult{}, err
	}
	return true, CommitResult{Asset: asset, Version: version, NoChanges: true}, nil
}

func pathExists(path string) error {
	return &rules.Error{Code: rules.CodePathExists, Message: fmt.Sprintf("Ya existe un archivo en «%s».", path)}
}

// CommitVersion crea una versión nueva de un archivo existente.
func (s *Service) CommitVersion(ctx context.Context, actor rules.Actor, in CommitInput) (CommitResult, error) {
	comment, err := rules.CheckComment(in.Comment)
	if err != nil {
		return CommitResult{}, err
	}
	if err := s.validateManifest(in.Manifest); err != nil {
		return CommitResult{}, err
	}
	if in.SizeBytes != in.Manifest.Size() {
		return CommitResult{}, &rules.Error{Code: rules.CodeInvalidManifest, Message: "El tamaño indicado no coincide con el manifiesto."}
	}
	sha := in.Manifest.FileSHA256.String()

	// Comprobación previa: evita verificar todo el archivo para rechazarlo después.
	var (
		result CommitResult
		done   bool
	)
	err = s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		done, result, err = s.checkCommit(ctx, tx, actor, in.AssetID, in.BaseVersion, sha)
		return err
	})
	if err != nil {
		return CommitResult{}, err
	}
	if done {
		return result, nil
	}

	manifestID, err := s.ensureManifest(ctx, in.Manifest)
	if err != nil {
		return CommitResult{}, err
	}
	if s.afterVerify != nil {
		s.afterVerify()
	}

	// Ahora sí, con el bloqueo de escritura: lo comprobado arriba pudo cambiar
	// mientras se verificaba el archivo.
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		done, result, err = s.checkCommit(ctx, tx, actor, in.AssetID, in.BaseVersion, sha)
		if err != nil || done {
			return err
		}
		asset := result.Asset

		version, err := s.insertVersion(ctx, tx, actor, newVersion{
			asset:      asset,
			manifestID: manifestID,
			fileSHA256: sha,
			sizeBytes:  in.Manifest.Size(),
			comment:    comment,
			action:     actionUpload,
			eventKind:  EventVersion,
		})
		if err != nil {
			return err
		}

		if in.ReleaseLock && asset.Lock != nil {
			if err := s.releaseLock(ctx, tx, actor, asset, false); err != nil {
				return err
			}
		}

		asset, err = loadAsset(ctx, tx, asset.ID)
		if err != nil {
			return err
		}
		result = CommitResult{Asset: asset, Version: version}
		return nil
	})
	if err != nil {
		return CommitResult{}, err
	}
	if !done {
		s.hub.Notify()
		s.thumbnailLater(result.Asset, result.Version)
		s.refsLater(result.Asset, result.Version)
	}
	return result, nil
}

// checkCommit aplica las reglas 2 y 6. "No hay cambios" va antes que la versión de
// partida a propósito: si una subida se confirmó pero el cliente no recibió la
// respuesta, su reintento parte de una versión vieja y aun así debe tener éxito.
func (s *Service) checkCommit(ctx context.Context, tx *sql.Tx, actor rules.Actor, assetID, base int64, sha string) (bool, CommitResult, error) {
	asset, err := loadAsset(ctx, tx, assetID)
	if err != nil {
		return false, CommitResult{}, err
	}
	if err := checkProjectWritable(ctx, tx, asset.ProjectID); err != nil {
		return false, CommitResult{}, err
	}
	if rules.SameContent(asset.FileSHA256, sha) {
		version, err := loadVersionByNumber(ctx, tx, asset.ID, asset.HeadVersion)
		if err != nil {
			return false, CommitResult{}, err
		}
		return true, CommitResult{Asset: asset, Version: version, NoChanges: true}, nil
	}
	if err := rules.CheckWrite(actor, asset.Lock, asset.HeadVersion, base); err != nil {
		return false, CommitResult{}, err
	}
	return false, CommitResult{Asset: asset}, nil
}

// Restore crea una versión nueva con el contenido de una anterior, sin duplicar
// bytes (regla 5).
func (s *Service) Restore(ctx context.Context, actor rules.Actor, assetID, number, base int64) (CommitResult, error) {
	var (
		result CommitResult
		done   bool
	)
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		target, err := loadVersionByNumber(ctx, tx, assetID, number)
		if err != nil {
			return err
		}
		done, result, err = s.checkCommit(ctx, tx, actor, assetID, base, target.FileSHA256)
		if err != nil || done {
			return err
		}
		asset := result.Asset

		restoredFrom := target.ID
		version, err := s.insertVersion(ctx, tx, actor, newVersion{
			asset:        asset,
			manifestID:   target.manifestID,
			fileSHA256:   target.FileSHA256,
			sizeBytes:    target.SizeBytes,
			comment:      rules.RestoreComment(number),
			restoredFrom: &restoredFrom,
			action:       actionRestore,
			eventKind:    EventVersion,
		})
		if err != nil {
			return err
		}
		asset, err = loadAsset(ctx, tx, assetID)
		if err != nil {
			return err
		}
		result = CommitResult{Asset: asset, Version: version}
		return nil
	})
	if err != nil {
		return CommitResult{}, err
	}
	if !done {
		s.hub.Notify()
		s.thumbnailLater(result.Asset, result.Version)
		s.refsLater(result.Asset, result.Version)
	}
	return result, nil
}

// Move cambia la ruta de un archivo y conserva su historial (regla 9).
func (s *Service) Move(ctx context.Context, actor rules.Actor, assetID, base int64, newPath string) (Asset, error) {
	path, err := rules.NormalizePath(newPath)
	if err != nil {
		return Asset{}, err
	}

	var (
		asset   Asset
		changed bool
	)
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		asset, err = loadAsset(ctx, tx, assetID)
		if err != nil {
			return err
		}
		if err := checkProjectWritable(ctx, tx, asset.ProjectID); err != nil {
			return err
		}
		if err := rules.CheckWrite(actor, asset.Lock, asset.HeadVersion, base); err != nil {
			return err
		}
		if err := rules.CheckExtension(asset.Ext, path.Ext); err != nil {
			return err
		}
		if asset.Path == path.Clean {
			return nil
		}
		if path.Key != asset.pathKey {
			var other int64
			err := tx.QueryRowContext(ctx, `
				SELECT id FROM assets WHERE project_id = ? AND path_key = ? AND deleted_at IS NULL`,
				asset.ProjectID, path.Key).Scan(&other)
			if err == nil {
				return pathExists(path.Clean)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("consultar ruta: %w", err)
			}
		}
		if err := checkFolderFree(ctx, tx, asset.ProjectID, path); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `UPDATE assets SET path = ?, path_key = ?, ext = ? WHERE id = ?`,
			path.Clean, path.Key, path.Ext, assetID); err != nil {
			return fmt.Errorf("mover archivo: %w", err)
		}
		if err := s.addActivity(ctx, tx, activityEntry{
			userID:    actor.UserID,
			action:    actionMove,
			projectID: asset.ProjectID,
			assetID:   assetID,
			detail:    asset.Path + " → " + path.Clean,
		}); err != nil {
			return err
		}
		if err := s.addEvent(ctx, tx, asset.ProjectID, EventMoved, assetID, eventPayload{Path: path.Clean, OldPath: asset.Path}); err != nil {
			return err
		}
		changed = true
		asset, err = loadAsset(ctx, tx, assetID)
		return err
	})
	if err != nil {
		return Asset{}, err
	}
	if changed {
		s.hub.Notify()
	}
	return asset, nil
}

// Delete hace un borrado suave: los datos siguen ahí hasta una purga del admin
// (regla 8). Reintentar el borrado de algo ya borrado no es un error de datos,
// pero el archivo se considera inexistente.
func (s *Service) Delete(ctx context.Context, actor rules.Actor, assetID, base int64) error {
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		asset, err := loadAsset(ctx, tx, assetID)
		if err != nil {
			return err
		}
		if err := checkProjectWritable(ctx, tx, asset.ProjectID); err != nil {
			return err
		}
		if err := rules.CheckWrite(actor, asset.Lock, asset.HeadVersion, base); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET deleted_at = ? WHERE id = ?`, millis(s.now()), assetID); err != nil {
			return fmt.Errorf("borrar archivo: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE asset_id = ?`, assetID); err != nil {
			return fmt.Errorf("liberar bloqueo: %w", err)
		}
		if err := s.addActivity(ctx, tx, activityEntry{
			userID:    actor.UserID,
			action:    actionDelete,
			projectID: asset.ProjectID,
			assetID:   assetID,
			detail:    asset.Path,
		}); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, asset.ProjectID, EventDeleted, assetID, eventPayload{Path: asset.Path})
	})
	if err != nil {
		return err
	}
	s.hub.Notify()
	return nil
}

// LockResult es el estado tras pedir un bloqueo.
type LockResult struct {
	Lock        *rules.Lock `json:"lock"`
	HeadVersion int64       `json:"head_version"`
}

// Lock bloquea un archivo para editarlo. takeover mueve a esta PC un bloqueo que
// el mismo usuario tiene en otra.
func (s *Service) Lock(ctx context.Context, actor rules.Actor, assetID int64, takeover bool) (LockResult, error) {
	var (
		result  LockResult
		changed bool
	)
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		asset, err := loadAsset(ctx, tx, assetID)
		if err != nil {
			return err
		}
		if err := checkProjectWritable(ctx, tx, asset.ProjectID); err != nil {
			return err
		}
		decision, err := rules.CheckAcquire(actor, asset.Lock, asset.HeadVersion, takeover)
		if err != nil {
			return err
		}
		if decision == rules.LockKeep {
			result = LockResult{Lock: asset.Lock, HeadVersion: asset.HeadVersion}
			return nil
		}

		now := s.now()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO locks(asset_id, user_id, locked_at, client_id, client_name) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(asset_id) DO UPDATE SET
				user_id = excluded.user_id, locked_at = excluded.locked_at,
				client_id = excluded.client_id, client_name = excluded.client_name`,
			assetID, actor.UserID, millis(now), actor.ClientID, actor.ClientName); err != nil {
			return fmt.Errorf("bloquear archivo: %w", err)
		}

		action := actionLock
		if decision == rules.LockTakeover {
			action = actionTakeover
		}
		if err := s.addActivity(ctx, tx, activityEntry{
			userID:    actor.UserID,
			action:    action,
			projectID: asset.ProjectID,
			assetID:   assetID,
			detail:    actor.ClientName,
		}); err != nil {
			return err
		}

		asset, err = loadAsset(ctx, tx, assetID)
		if err != nil {
			return err
		}
		if err := s.addEvent(ctx, tx, asset.ProjectID, EventLocked, assetID, eventPayload{Path: asset.Path, Lock: asset.Lock}); err != nil {
			return err
		}
		changed = true
		result = LockResult{Lock: asset.Lock, HeadVersion: asset.HeadVersion}
		return nil
	})
	if err != nil {
		return LockResult{}, err
	}
	if changed {
		s.hub.Notify()
	}
	return result, nil
}

// Unlock libera un bloqueo. force permite a un admin liberar el de otro, y queda
// registrado (regla 4).
func (s *Service) Unlock(ctx context.Context, actor rules.Actor, assetID int64, force bool) error {
	var changed bool
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		asset, err := loadAsset(ctx, tx, assetID)
		if err != nil {
			return err
		}
		if err := rules.CheckRelease(actor, asset.Lock, force); err != nil {
			return err
		}
		if asset.Lock == nil {
			return nil
		}
		changed = true
		return s.releaseLock(ctx, tx, actor, asset, rules.IsForcedRelease(actor, asset.Lock))
	})
	if err != nil {
		return err
	}
	if changed {
		s.hub.Notify()
	}
	return nil
}

func (s *Service) releaseLock(ctx context.Context, tx *sql.Tx, actor rules.Actor, asset Asset, forced bool) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE asset_id = ?`, asset.ID); err != nil {
		return fmt.Errorf("liberar bloqueo: %w", err)
	}
	action, detail := actionUnlock, ""
	if forced {
		action = actionForceUnlock
		detail = "Bloqueo de " + asset.Lock.DisplayName + " en " + asset.Lock.ClientName
	}
	if err := s.addActivity(ctx, tx, activityEntry{
		userID:    actor.UserID,
		action:    action,
		projectID: asset.ProjectID,
		assetID:   asset.ID,
		detail:    detail,
	}); err != nil {
		return err
	}
	return s.addEvent(ctx, tx, asset.ProjectID, EventUnlocked, asset.ID, eventPayload{Path: asset.Path, Forced: forced})
}

const versionColumns = `
	v.id, v.asset_id, v.number, v.file_sha256, v.size_bytes, v.manifest_id, v.comment,
	v.uploaded_by, u.display_name, v.created_at, v.restored_from`

const versionFrom = ` FROM versions v JOIN users u ON u.id = v.uploaded_by`

func scanVersion(row scanner) (Version, error) {
	var (
		v         Version
		createdAt int64
		restored  sql.NullInt64
	)
	err := row.Scan(&v.ID, &v.AssetID, &v.Number, &v.FileSHA256, &v.SizeBytes, &v.manifestID, &v.Comment,
		&v.UploadedByID, &v.UploadedBy, &createdAt, &restored)
	if err != nil {
		return Version{}, err
	}
	v.CreatedAt = fromMillis(createdAt)
	if restored.Valid {
		v.RestoredFrom = &restored.Int64
	}
	return v, nil
}

func loadVersionByNumber(ctx context.Context, tx *sql.Tx, assetID, number int64) (Version, error) {
	v, err := scanVersion(tx.QueryRowContext(ctx, `SELECT `+versionColumns+versionFrom+` WHERE v.asset_id = ? AND v.number = ?`, assetID, number))
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, notFound(fmt.Sprintf("La versión v%d", number))
	}
	if err != nil {
		return Version{}, fmt.Errorf("leer versión: %w", err)
	}
	return v, nil
}

// ListVersions devuelve el historial de un archivo, de la más nueva a la más vieja.
func (s *Service) ListVersions(ctx context.Context, assetID int64) ([]Version, error) {
	versions := []Version{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		if _, err := loadAsset(ctx, tx, assetID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+versionColumns+versionFrom+` WHERE v.asset_id = ? ORDER BY v.number DESC`, assetID)
		if err != nil {
			return fmt.Errorf("listar versiones: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				return fmt.Errorf("leer versión: %w", err)
			}
			versions = append(versions, v)
		}
		return rows.Err()
	})
	return versions, err
}

// VersionFile es lo necesario para entregar una versión.
type VersionFile struct {
	Version  Version
	Path     string
	Manifest manifest.Manifest
}

// GetVersionFile devuelve una versión con su ruta y su manifiesto. Cualquier
// usuario puede ver y descargar cualquier versión, incluso de archivos borrados
// (regla 1 y borrado suave).
func (s *Service) GetVersionFile(ctx context.Context, versionID int64) (VersionFile, error) {
	var file VersionFile
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		file.Version, err = scanVersion(tx.QueryRowContext(ctx, `SELECT `+versionColumns+versionFrom+` WHERE v.id = ?`, versionID))
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("La versión")
		}
		if err != nil {
			return fmt.Errorf("leer versión: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT path FROM assets WHERE id = ?`, file.Version.AssetID).Scan(&file.Path); err != nil {
			return fmt.Errorf("leer archivo: %w", err)
		}
		file.Manifest, err = loadManifest(ctx, tx, file.Version.manifestID)
		return err
	})
	return file, err
}

func loadManifest(ctx context.Context, tx *sql.Tx, id int64) (manifest.Manifest, error) {
	var (
		m    manifest.Manifest
		file string
	)
	if err := tx.QueryRowContext(ctx, `SELECT file_sha256 FROM manifests WHERE id = ?`, id).Scan(&file); err != nil {
		return manifest.Manifest{}, fmt.Errorf("leer manifiesto: %w", err)
	}
	var err error
	if m.FileSHA256, err = chunker.ParseHash(file); err != nil {
		return manifest.Manifest{}, fmt.Errorf("hash del manifiesto: %w", err)
	}

	rows, err := tx.QueryContext(ctx, `SELECT chunk_sha256, size FROM manifest_chunks WHERE manifest_id = ? ORDER BY idx`, id)
	if err != nil {
		return manifest.Manifest{}, fmt.Errorf("leer trozos del manifiesto: %w", err)
	}
	defer rows.Close()
	m.Chunks = []manifest.Chunk{}
	for rows.Next() {
		var (
			hash string
			c    manifest.Chunk
		)
		if err := rows.Scan(&hash, &c.Size); err != nil {
			return manifest.Manifest{}, fmt.Errorf("leer trozo del manifiesto: %w", err)
		}
		if c.SHA256, err = chunker.ParseHash(hash); err != nil {
			return manifest.Manifest{}, fmt.Errorf("hash de trozo: %w", err)
		}
		m.Chunks = append(m.Chunks, c)
	}
	return m, rows.Err()
}

// ReadChunkData devuelve el contenido de un trozo de un manifiesto ya registrado.
func (s *Service) ReadChunkData(h chunker.Hash) ([]byte, error) {
	data, err := s.store.Get(h)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("falta un trozo registrado %s: %w", h, err)
	}
	return data, err
}

// cleanDisplayName recorta un nombre visible y usa fallback si queda vacío.
func cleanDisplayName(name, fallback string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	return fallback
}

// VersionDetail es una versión con lo que cambió respecto a la anterior.
type VersionDetail struct {
	Version
	ChangedBytes int64
	// ChangedPercent es ChangedBytes como porcentaje del tamaño de la versión.
	ChangedPercent float64
}

// maxVersionDetails limita el coste de comparar trozos en la ficha de un archivo.
const maxVersionDetails = 100

// ListVersionDetails devuelve las últimas versiones, de la más nueva a la más
// vieja, indicando qué parte hubo que transferir a quien ya tenía la anterior.
func (s *Service) ListVersionDetails(ctx context.Context, assetID int64) ([]VersionDetail, error) {
	details := []VersionDetail{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		if _, err := loadAsset(ctx, tx, assetID); err != nil {
			return err
		}
		// Una más de las que se muestran, para poder comparar la más vieja.
		rows, err := tx.QueryContext(ctx, `SELECT `+versionColumns+versionFrom+` WHERE v.asset_id = ? ORDER BY v.number DESC LIMIT ?`, assetID, maxVersionDetails+1)
		if err != nil {
			return fmt.Errorf("listar versiones: %w", err)
		}
		var versions []Version
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("leer versión: %w", err)
			}
			versions = append(versions, v)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("leer versiones: %w", err)
		}

		// Varias versiones pueden compartir manifiesto: se lee cada uno una vez.
		sets := make(map[int64]map[string]int64)
		for _, v := range versions {
			if _, ok := sets[v.manifestID]; ok {
				continue
			}
			set, err := manifestChunkSet(ctx, tx, v.manifestID)
			if err != nil {
				return err
			}
			sets[v.manifestID] = set
		}

		for i, v := range versions {
			if i == maxVersionDetails {
				break
			}
			var previous map[string]int64
			if i+1 < len(versions) {
				previous = sets[versions[i+1].manifestID]
			}
			var changed int64
			for hash, size := range sets[v.manifestID] {
				if _, ok := previous[hash]; !ok {
					changed += size
				}
			}
			detail := VersionDetail{Version: v, ChangedBytes: changed}
			if v.SizeBytes > 0 {
				detail.ChangedPercent = float64(changed) / float64(v.SizeBytes) * 100
			}
			details = append(details, detail)
		}
		return nil
	})
	return details, err
}

func manifestChunkSet(ctx context.Context, tx *sql.Tx, manifestID int64) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT chunk_sha256, size FROM manifest_chunks WHERE manifest_id = ?`, manifestID)
	if err != nil {
		return nil, fmt.Errorf("leer trozos: %w", err)
	}
	defer rows.Close()
	set := make(map[string]int64)
	for rows.Next() {
		var (
			hash string
			size int64
		)
		if err := rows.Scan(&hash, &size); err != nil {
			return nil, fmt.Errorf("leer trozo: %w", err)
		}
		set[hash] = size
	}
	return set, rows.Err()
}
