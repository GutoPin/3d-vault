// Package rules contiene las reglas de negocio de la sección 6 de PLAN.md como
// funciones puras: sin base de datos ni red, para poder probarlas una a una.
package rules

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Code identifica un motivo de rechazo. Es estable: los clientes lo interpretan.
type Code string

// Motivos de rechazo.
const (
	CodeInvalidComment    Code = "invalid_comment"
	CodeInvalidPath       Code = "invalid_path"
	CodeInvalidManifest   Code = "invalid_manifest"
	CodeInvalidInput      Code = "invalid_input"
	CodeMissingChunks     Code = "missing_chunks"
	CodeManifestMismatch  Code = "manifest_mismatch"
	CodeLocked            Code = "locked"
	CodeLockedOtherClient Code = "locked_other_client"
	CodeStaleVersion      Code = "stale_version"
	CodePathExists        Code = "path_exists"
	CodeNameExists        Code = "name_exists"
	CodeExtensionChanged  Code = "extension_changed"
	CodeNotLockOwner      Code = "not_lock_owner"
	CodeForbidden         Code = "forbidden"
	CodeNotFound          Code = "not_found"
	CodeLastAdmin         Code = "last_admin"
	CodeProjectArchived   Code = "project_archived"
)

// Error es un rechazo por una regla de negocio, con un mensaje para mostrar.
type Error struct {
	Code        Code     `json:"code"`
	Message     string   `json:"message"`
	HeadVersion int64    `json:"head_version,omitempty"`
	Lock        *Lock    `json:"lock,omitempty"`
	Missing     []string `json:"missing,omitempty"`
}

func (e *Error) Error() string {
	return e.Message
}

// Lock indica quién está editando un archivo y desde qué PC.
type Lock struct {
	UserID      int64     `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	ClientID    string    `json:"client_id"`
	ClientName  string    `json:"client_name"`
	LockedAt    time.Time `json:"locked_at"`
}

// Actor es quien realiza una acción.
type Actor struct {
	UserID     int64
	IsAdmin    bool
	ClientID   string
	ClientName string
}

const (
	minCommentRunes = 3
	maxCommentRunes = 2000
	lockDateFormat  = "02/01/2006 15:04"
)

// CheckComment valida el comentario de una versión (regla 3) y lo devuelve sin
// espacios sobrantes.
func CheckComment(comment string) (string, error) {
	trimmed := strings.TrimSpace(comment)
	count := utf8.RuneCountInString(trimmed)
	if count < minCommentRunes {
		return "", &Error{Code: CodeInvalidComment, Message: fmt.Sprintf("El comentario debe tener al menos %d caracteres.", minCommentRunes)}
	}
	if count > maxCommentRunes {
		return "", &Error{Code: CodeInvalidComment, Message: fmt.Sprintf("El comentario no puede superar los %d caracteres.", maxCommentRunes)}
	}
	return trimmed, nil
}

func lockedByOther(lock *Lock, head int64) *Error {
	return &Error{
		Code:        CodeLocked,
		Message:     fmt.Sprintf("Bloqueado por %s desde %s.", lock.DisplayName, lock.LockedAt.Format(lockDateFormat)),
		HeadVersion: head,
		Lock:        lock,
	}
}

func lockedOnOtherClient(lock *Lock, head int64) *Error {
	return &Error{
		Code:        CodeLockedOtherClient,
		Message:     fmt.Sprintf("Lo tienes bloqueado en otra PC (%s). Toma el bloqueo desde esta PC para continuar.", lock.ClientName),
		HeadVersion: head,
		Lock:        lock,
	}
}

// CheckWrite decide si actor puede cambiar un archivo existente: subir, restaurar,
// mover o borrar (reglas 2, 5, 8 y 9). lock es nil si el archivo está libre.
//
// base es la versión sobre la que el actor hizo su cambio. Sin esta comprobación,
// una subida sin bloqueo basada en una versión vieja desplazaría trabajo más nuevo.
func CheckWrite(actor Actor, lock *Lock, head, base int64) error {
	if lock != nil {
		if lock.UserID != actor.UserID {
			return lockedByOther(lock, head)
		}
		if lock.ClientID != actor.ClientID {
			return lockedOnOtherClient(lock, head)
		}
	}
	if base != head {
		return &Error{
			Code:        CodeStaleVersion,
			Message:     fmt.Sprintf("Hay una versión más nueva (v%d) que la que estás usando (v%d). Actualiza el archivo antes de continuar.", head, base),
			HeadVersion: head,
			Lock:        lock,
		}
	}
	return nil
}

// LockDecision es el resultado de pedir un bloqueo.
type LockDecision int

// Resultados posibles de CheckAcquire.
const (
	// LockAcquire crea el bloqueo: el archivo estaba libre.
	LockAcquire LockDecision = iota + 1
	// LockKeep no cambia nada: el actor ya lo tiene en esta PC.
	LockKeep
	// LockTakeover mueve el bloqueo del actor a esta PC.
	LockTakeover
)

// CheckAcquire decide si actor puede bloquear un archivo. Mover un bloqueo propio
// entre PCs requiere pedirlo de forma explícita, porque la otra PC puede tener
// cambios sin subir.
func CheckAcquire(actor Actor, lock *Lock, head int64, takeover bool) (LockDecision, error) {
	switch {
	case lock == nil:
		return LockAcquire, nil
	case lock.UserID != actor.UserID:
		return 0, lockedByOther(lock, head)
	case lock.ClientID == actor.ClientID:
		return LockKeep, nil
	case takeover:
		return LockTakeover, nil
	default:
		return 0, lockedOnOtherClient(lock, head)
	}
}

// CheckRelease decide si actor puede liberar un bloqueo (regla 4). Liberar un
// archivo ya libre no es un error, para que reintentar sea seguro.
func CheckRelease(actor Actor, lock *Lock, force bool) error {
	if lock == nil || lock.UserID == actor.UserID {
		return nil
	}
	if !force {
		return &Error{
			Code:    CodeNotLockOwner,
			Message: fmt.Sprintf("Solo %s puede liberar este bloqueo.", lock.DisplayName),
			Lock:    lock,
		}
	}
	if !actor.IsAdmin {
		return &Error{Code: CodeForbidden, Message: "Solo un administrador puede forzar el desbloqueo."}
	}
	return nil
}

// IsForcedRelease indica si la liberación debe quedar registrada como forzada.
func IsForcedRelease(actor Actor, lock *Lock) bool {
	return lock != nil && lock.UserID != actor.UserID
}

// SameContent indica si una subida no cambia el archivo (regla 6).
func SameContent(headSHA256, newSHA256 string) bool {
	return headSHA256 == newSHA256
}

// CheckExtension exige conservar la extensión (regla 7).
func CheckExtension(oldExt, newExt string) error {
	if !strings.EqualFold(oldExt, newExt) {
		return &Error{
			Code:    CodeExtensionChanged,
			Message: fmt.Sprintf("La extensión no puede cambiar (%q a %q).", oldExt, newExt),
		}
	}
	return nil
}

// RestoreComment es el comentario de una versión restaurada (regla 5).
func RestoreComment(number int64) string {
	return fmt.Sprintf("Restaurado desde v%d", number)
}
