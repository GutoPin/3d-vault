package vault

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"3d-vault/internal/rules"
)

// EventUnlockRequested avisa a quien tiene un archivo de que otra persona lo
// necesita. No cambia nada: Mi carpeta lo muestra como aviso.
const EventUnlockRequested = "unlock_requested"

const (
	actionRequestUnlock = "pedir_liberar"
	// requestEvery evita que alguien llene de avisos a quien tiene el archivo.
	requestEvery = 5 * time.Minute
)

// RequestUnlock pide a quien tiene bloqueado un archivo que lo libere. Queda en
// la actividad y llega como evento a sus PCs.
func (s *Service) RequestUnlock(ctx context.Context, actor rules.Actor, assetID int64) error {
	user, err := s.GetUser(ctx, actor.UserID)
	if err != nil {
		return err
	}
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		asset, err := loadAsset(ctx, tx, assetID)
		if err != nil {
			return err
		}
		switch {
		case asset.Lock == nil:
			return &rules.Error{Code: rules.CodeInvalidInput, Message: "El archivo no está bloqueado: ya puedes bloquearlo tú."}
		case asset.Lock.UserID == actor.UserID:
			return &rules.Error{Code: rules.CodeInvalidInput, Message: "El bloqueo ya es tuyo."}
		}
		var recent bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM activity WHERE user_id = ? AND asset_id = ? AND action = ? AND created_at > ?)`,
			actor.UserID, assetID, actionRequestUnlock, millis(s.now().Add(-requestEvery))).Scan(&recent); err != nil {
			return fmt.Errorf("revisar peticiones recientes: %w", err)
		}
		if recent {
			return &rules.Error{Code: rules.CodeInvalidInput, Message: "Ya se lo pediste hace poco. Espera unos minutos antes de volver a pedirlo."}
		}
		if err := s.addActivity(ctx, tx, activityEntry{
			userID:    actor.UserID,
			action:    actionRequestUnlock,
			projectID: asset.ProjectID,
			assetID:   assetID,
			detail:    asset.Lock.DisplayName,
		}); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, asset.ProjectID, EventUnlockRequested, assetID, eventPayload{
			Path: asset.Path, ActorName: user.DisplayName, Lock: asset.Lock,
		})
	})
	if err != nil {
		return fmt.Errorf("pedir que liberen: %w", err)
	}
	s.hub.Notify()
	return nil
}
