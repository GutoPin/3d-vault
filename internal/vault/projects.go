package vault

import (
	"context"
	"database/sql"
	"fmt"

	"3d-vault/internal/rules"
)

// Eventos y acciones de proyectos.
const (
	EventProjectDeleted  = "project_deleted"
	EventProjectRestored = "project_restored"

	actionDeleteProject  = "borrar_proyecto"
	actionRestoreProject = "recuperar_proyecto"
)

// DeleteProject borra un proyecto: deja de aparecer en las listas y en Mi
// carpeta y no admite cambios, pero conserva todos sus archivos y versiones y
// se puede recuperar con RestoreProject. Cualquier usuario puede hacerlo,
// salvo que otra persona tenga bloqueado algún archivo: puede estar editándolo.
func (s *Service) DeleteProject(ctx context.Context, actor rules.Actor, projectID int64) error {
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		p, err := loadProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if p.Archived {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+assetColumns+assetFrom+` AND a.project_id = ? AND l.asset_id IS NOT NULL ORDER BY a.path_key`, projectID)
		if err != nil {
			return fmt.Errorf("consultar bloqueos: %w", err)
		}
		var locked []Asset
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("leer archivo: %w", err)
			}
			locked = append(locked, a)
		}
		rows.Close()
		if others := lockedByOthers(actor, locked); others != nil {
			return lockedError("borrar el proyecto", others)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET archived = 1, deleted_at = ?, deleted_by = ? WHERE id = ?`,
			millis(s.now()), actor.UserID, projectID); err != nil {
			return fmt.Errorf("borrar proyecto: %w", err)
		}
		if err := s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionDeleteProject, projectID: projectID, detail: p.Name}); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, projectID, EventProjectDeleted, 0, eventPayload{Path: p.Name})
	})
	if err != nil {
		return err
	}
	s.hub.Notify()
	return nil
}

// RestoreProject recupera un proyecto borrado, tal como estaba.
func (s *Service) RestoreProject(ctx context.Context, actor rules.Actor, projectID int64) error {
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		p, err := loadProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if !p.Archived {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET archived = 0, deleted_at = NULL, deleted_by = NULL WHERE id = ?`, projectID); err != nil {
			return fmt.Errorf("recuperar proyecto: %w", err)
		}
		if err := s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionRestoreProject, projectID: projectID, detail: p.Name}); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, projectID, EventProjectRestored, 0, eventPayload{Path: p.Name})
	})
	if err != nil {
		return err
	}
	s.hub.Notify()
	return nil
}
