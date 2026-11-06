package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"3d-vault/internal/auth"
	"3d-vault/internal/rules"
)

const (
	minPasswordRunes = 10
	maxPasswordBytes = 1024
	maxNameRunes     = 100
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

// User es una cuenta. El admin crea las cuentas: no hay registro público.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	IsAdmin      bool      `json:"is_admin"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	PasswordHash string    `json:"-"`
}

// NewUserInput describe una cuenta nueva.
type NewUserInput struct {
	Username    string
	DisplayName string
	Password    string
	IsAdmin     bool
}

func checkPassword(password string) error {
	if utf8.RuneCountInString(password) < minPasswordRunes {
		return invalidInput(fmt.Sprintf("La contraseña debe tener al menos %d caracteres.", minPasswordRunes))
	}
	if len(password) > maxPasswordBytes {
		return invalidInput("La contraseña es demasiado larga.")
	}
	return nil
}

func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// CreateUser crea una cuenta. actorID es 0 cuando la crea un comando de consola,
// como create-admin: en ese caso la actividad se atribuye a la cuenta creada.
func (s *Service) CreateUser(ctx context.Context, actorID int64, in NewUserInput) (User, error) {
	username := normalizeUsername(in.Username)
	if !usernamePattern.MatchString(username) {
		return User{}, invalidInput("El usuario debe tener de 2 a 32 caracteres: letras, números, puntos, guiones o guiones bajos.")
	}
	display := cleanDisplayName(in.DisplayName, username)
	if utf8.RuneCountInString(display) > maxNameRunes {
		return User{}, invalidInput(fmt.Sprintf("El nombre no puede superar los %d caracteres.", maxNameRunes))
	}
	if err := checkPassword(in.Password); err != nil {
		return User{}, err
	}
	hash, err := auth.HashPassword(s.argon, in.Password)
	if err != nil {
		return User{}, err
	}

	now := s.now()
	user := User{Username: username, DisplayName: display, IsAdmin: in.IsAdmin, IsActive: true, CreatedAt: fromMillis(millis(now)), PasswordHash: hash}
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE username = ?`, username).Scan(&exists)
		if err == nil {
			return &rules.Error{Code: rules.CodeNameExists, Message: fmt.Sprintf("El usuario «%s» ya existe.", username)}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("consultar usuario: %w", err)
		}

		res, err := tx.ExecContext(ctx, `
			INSERT INTO users(username, display_name, password_hash, is_admin, is_active, created_at) VALUES (?, ?, ?, ?, 1, ?)`,
			username, display, hash, in.IsAdmin, millis(now))
		if err != nil {
			return fmt.Errorf("crear usuario: %w", err)
		}
		user.ID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("leer id del usuario: %w", err)
		}

		author := actorID
		if author == 0 {
			author = user.ID
		}
		return s.addActivity(ctx, tx, activityEntry{userID: author, action: actionCreateUser, detail: username})
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

const userColumns = `id, username, display_name, is_admin, is_active, created_at, password_hash`

func scanUser(row scanner) (User, error) {
	var (
		u         User
		createdAt int64
	)
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.IsActive, &createdAt, &u.PasswordHash); err != nil {
		return User{}, err
	}
	u.CreatedAt = fromMillis(createdAt)
	return u, nil
}

// GetUser devuelve una cuenta.
func (s *Service) GetUser(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, notFound("El usuario")
	}
	if err != nil {
		return User{}, fmt.Errorf("leer usuario: %w", err)
	}
	return u, nil
}

// ListUsers devuelve las cuentas por nombre de usuario.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	users := []User{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY username`)
		if err != nil {
			return fmt.Errorf("listar usuarios: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				return fmt.Errorf("leer usuario: %w", err)
			}
			users = append(users, u)
		}
		return rows.Err()
	})
	return users, err
}

// HasAdmin indica si existe algún administrador activo.
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	var count int
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1 AND is_active = 1`).Scan(&count)
	})
	if err != nil {
		return false, fmt.Errorf("contar administradores: %w", err)
	}
	return count > 0, nil
}

// dummyPasswordHash se verifica cuando el usuario no existe, para que el tiempo de
// respuesta no revele qué usuarios existen.
func (s *Service) dummyPasswordHash() (string, error) {
	return auth.HashPassword(s.argon, "usuario-inexistente")
}

// Authenticate comprueba usuario y contraseña. Devuelve ok=false sin distinguir si
// falló el usuario, la contraseña o si la cuenta está desactivada.
func (s *Service) Authenticate(ctx context.Context, username, password string) (User, bool, error) {
	if len(password) > maxPasswordBytes {
		return User{}, false, nil
	}
	var u User
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, normalizeUsername(username)))
		return err
	})
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, false, fmt.Errorf("leer usuario: %w", err)
	}

	hash := u.PasswordHash
	if !exists {
		if hash, err = s.dummyPasswordHash(); err != nil {
			return User{}, false, err
		}
	}
	matches, err := auth.VerifyPassword(hash, password)
	if err != nil {
		return User{}, false, fmt.Errorf("verificar contraseña: %w", err)
	}
	if !exists || !matches || !u.IsActive {
		return User{}, false, nil
	}
	return u, true, nil
}

// SetActive activa o desactiva una cuenta. Sus bloqueos siguen vigentes hasta que
// un admin los fuerce: pueden proteger trabajo sin subir.
func (s *Service) SetActive(ctx context.Context, actorID, userID int64, active bool) error {
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		target, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, userID))
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("El usuario")
		}
		if err != nil {
			return fmt.Errorf("leer usuario: %w", err)
		}
		if !active && target.IsAdmin {
			var admins int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1 AND is_active = 1`).Scan(&admins); err != nil {
				return fmt.Errorf("contar administradores: %w", err)
			}
			if admins <= 1 && target.IsActive {
				return &rules.Error{Code: rules.CodeLastAdmin, Message: "No se puede desactivar al único administrador."}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET is_active = ? WHERE id = ?`, active, userID); err != nil {
			return fmt.Errorf("actualizar usuario: %w", err)
		}
		detail := target.Username + ": desactivado"
		if active {
			detail = target.Username + ": activado"
		}
		return s.addActivity(ctx, tx, activityEntry{userID: actorID, action: actionUpdateUser, detail: detail})
	})
}

// SetPassword cambia una contraseña. Esto invalida las sesiones del usuario y
// revoca sus tokens: si se cambia, es porque la anterior pudo verse comprometida.
func (s *Service) SetPassword(ctx context.Context, actorID, userID int64, password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(s.argon, password)
	if err != nil {
		return err
	}
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
		if err != nil {
			return fmt.Errorf("actualizar contraseña: %w", err)
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("actualizar contraseña: %w", err)
		}
		if changed == 0 {
			return notFound("El usuario")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, millis(s.now()), userID); err != nil {
			return fmt.Errorf("revocar tokens: %w", err)
		}
		return s.addActivity(ctx, tx, activityEntry{userID: actorID, action: actionSetPassword, detail: fmt.Sprintf("usuario %d", userID)})
	})
}

// Token es un token de API, sin su valor.
type Token struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// CreateToken crea un token y devuelve su valor, que no se puede volver a consultar.
func (s *Service) CreateToken(ctx context.Context, userID int64, name string) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return Token{}, "", invalidInput(fmt.Sprintf("El nombre del token debe tener de 1 a %d caracteres.", maxNameRunes))
	}
	plain, hash, err := auth.NewToken()
	if err != nil {
		return Token{}, "", err
	}
	now := s.now()
	token := Token{UserID: userID, Name: name, CreatedAt: fromMillis(millis(now))}
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO api_tokens(user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?)`,
			userID, name, hash, millis(now))
		if err != nil {
			return fmt.Errorf("crear token: %w", err)
		}
		token.ID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("leer id del token: %w", err)
		}
		return s.addActivity(ctx, tx, activityEntry{userID: userID, action: actionCreateToken, detail: name})
	})
	if err != nil {
		return Token{}, "", err
	}
	return token, plain, nil
}

// RevokeToken revoca un token propio, o de cualquiera si el actor es admin.
func (s *Service) RevokeToken(ctx context.Context, actor rules.Actor, tokenID int64) error {
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var owner int64
		err := tx.QueryRowContext(ctx, `SELECT user_id FROM api_tokens WHERE id = ?`, tokenID).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("El token")
		}
		if err != nil {
			return fmt.Errorf("leer token: %w", err)
		}
		if owner != actor.UserID && !actor.IsAdmin {
			// Igual que si no existiera: no revela tokens de otros.
			return notFound("El token")
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, millis(s.now()), tokenID); err != nil {
			return fmt.Errorf("revocar token: %w", err)
		}
		return s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionRevokeToken, detail: fmt.Sprintf("token %d", tokenID)})
	})
}

const tokenTouchInterval = time.Minute

// UserByToken devuelve el dueño de un token vigente. La última fecha de uso se
// actualiza como mucho una vez por minuto, para no convertir cada lectura en una
// escritura.
func (s *Service) UserByToken(ctx context.Context, plain string) (User, bool, error) {
	hash := auth.HashToken(plain)
	var (
		u        User
		tokenID  int64
		lastUsed sql.NullInt64
	)
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT t.id, t.last_used_at, u.id, u.username, u.display_name, u.is_admin, u.is_active, u.created_at, u.password_hash
			FROM api_tokens t JOIN users u ON u.id = t.user_id
			WHERE t.token_hash = ? AND t.revoked_at IS NULL AND u.is_active = 1`, hash).Scan(
			&tokenID, &lastUsed, &u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.IsActive, new(int64), &u.PasswordHash)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("leer token: %w", err)
	}

	now := s.now()
	if !lastUsed.Valid || now.Sub(fromMillis(lastUsed.Int64)) >= tokenTouchInterval {
		err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, millis(now), tokenID)
			return err
		})
		if err != nil {
			return User{}, false, fmt.Errorf("actualizar uso del token: %w", err)
		}
	}
	return u, true, nil
}

// ListTokens devuelve los tokens vigentes de un usuario, el más nuevo primero.
func (s *Service) ListTokens(ctx context.Context, userID int64) ([]Token, error) {
	tokens := []Token{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, name, created_at, last_used_at FROM api_tokens
			WHERE user_id = ? AND revoked_at IS NULL ORDER BY id DESC`, userID)
		if err != nil {
			return fmt.Errorf("listar tokens: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				t         = Token{UserID: userID}
				createdAt int64
				lastUsed  sql.NullInt64
			)
			if err := rows.Scan(&t.ID, &t.Name, &createdAt, &lastUsed); err != nil {
				return fmt.Errorf("leer token: %w", err)
			}
			t.CreatedAt = fromMillis(createdAt)
			if lastUsed.Valid {
				used := fromMillis(lastUsed.Int64)
				t.LastUsedAt = &used
			}
			tokens = append(tokens, t)
		}
		return rows.Err()
	})
	return tokens, err
}
