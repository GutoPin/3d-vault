package vault

import (
	"testing"

	"3d-vault/internal/rules"
)

func TestCreateUserValidates(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	tests := []struct {
		name string
		in   NewUserInput
		code rules.Code
	}{
		{"usuario corto", NewUserInput{Username: "a", Password: "contraseña-larga"}, rules.CodeInvalidInput},
		{"caracteres raros", NewUserInput{Username: "ju an", Password: "contraseña-larga"}, rules.CodeInvalidInput},
		{"contraseña corta", NewUserInput{Username: "nuevo", Password: "corta"}, rules.CodeInvalidInput},
		{"repetido", NewUserInput{Username: "juan", Password: "contraseña-larga"}, rules.CodeNameExists},
		{"repetido en mayúsculas", NewUserInput{Username: " JUAN ", Password: "contraseña-larga"}, rules.CodeNameExists},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.CreateUser(e.ctx, e.admin.UserID, tt.in)
			wantRule(t, err, tt.code)
		})
	}
}

func TestAuthenticate(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	if _, ok, err := e.svc.Authenticate(e.ctx, "juan", "contraseña-larga"); err != nil || !ok {
		t.Fatalf("correcto: ok=%v err=%v", ok, err)
	}
	if u, ok, err := e.svc.Authenticate(e.ctx, " JUAN ", "contraseña-larga"); err != nil || !ok || u.Username != "juan" {
		t.Fatalf("mayúsculas: %+v ok=%v err=%v", u, ok, err)
	}
	if _, ok, err := e.svc.Authenticate(e.ctx, "juan", "otra"); err != nil || ok {
		t.Fatalf("contraseña mala: ok=%v err=%v", ok, err)
	}
	if _, ok, err := e.svc.Authenticate(e.ctx, "nadie", "contraseña-larga"); err != nil || ok {
		t.Fatalf("usuario inexistente: ok=%v err=%v", ok, err)
	}

	if err := e.svc.SetActive(e.ctx, e.admin.UserID, e.juan.UserID, false); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if _, ok, err := e.svc.Authenticate(e.ctx, "juan", "contraseña-larga"); err != nil || ok {
		t.Fatalf("desactivado: ok=%v err=%v", ok, err)
	}
}

func TestLastAdminCannotBeDeactivated(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	wantRule(t, e.svc.SetActive(e.ctx, e.admin.UserID, e.admin.UserID, false), rules.CodeLastAdmin)

	second := e.newActor("otra", "Otra", true, "pc-9", "PC")
	if err := e.svc.SetActive(e.ctx, second.UserID, e.admin.UserID, false); err != nil {
		t.Fatalf("con otro admin sí se puede: %v", err)
	}
	if has, err := e.svc.HasAdmin(e.ctx); err != nil || !has {
		t.Fatalf("HasAdmin = %v, %v", has, err)
	}
	wantRule(t, e.svc.SetActive(e.ctx, second.UserID, 999, false), rules.CodeNotFound)
}

func TestTokens(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	token, plain, err := e.svc.CreateToken(e.ctx, e.juan.UserID, "  PC de Juan ")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if token.Name != "PC de Juan" {
		t.Fatalf("nombre = %q", token.Name)
	}
	u, ok, err := e.svc.UserByToken(e.ctx, plain)
	if err != nil || !ok || u.ID != e.juan.UserID {
		t.Fatalf("UserByToken = %+v, %v, %v", u, ok, err)
	}
	if _, ok, _ := e.svc.UserByToken(e.ctx, plain+"x"); ok {
		t.Fatal("un token alterado no debe valer")
	}

	_, _, err = e.svc.CreateToken(e.ctx, e.juan.UserID, "   ")
	wantRule(t, err, rules.CodeInvalidInput)

	// Solo su dueño o un admin pueden revocarlo.
	wantRule(t, e.svc.RevokeToken(e.ctx, e.ana, token.ID), rules.CodeNotFound)
	if err := e.svc.RevokeToken(e.ctx, e.juan, token.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, ok, _ := e.svc.UserByToken(e.ctx, plain); ok {
		t.Fatal("un token revocado no debe valer")
	}

	second, secondPlain, err := e.svc.CreateToken(e.ctx, e.juan.UserID, "Otro")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeToken(e.ctx, e.admin, second.ID); err != nil {
		t.Fatalf("revocar como admin: %v", err)
	}
	if _, ok, _ := e.svc.UserByToken(e.ctx, secondPlain); ok {
		t.Fatal("un token revocado no debe valer")
	}
}

func TestTokensStopWorkingWithTheAccount(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	_, plain, err := e.svc.CreateToken(e.ctx, e.ana.UserID, "PC")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetActive(e.ctx, e.admin.UserID, e.ana.UserID, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.svc.UserByToken(e.ctx, plain); ok {
		t.Fatal("el token de una cuenta desactivada no debe valer")
	}
}

func TestChangingPasswordRevokesTokens(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	_, plain, err := e.svc.CreateToken(e.ctx, e.ana.UserID, "PC")
	if err != nil {
		t.Fatal(err)
	}
	wantRule(t, e.svc.SetPassword(e.ctx, e.ana.UserID, e.ana.UserID, "corta"), rules.CodeInvalidInput)
	if err := e.svc.SetPassword(e.ctx, e.ana.UserID, e.ana.UserID, "otra-contraseña-larga"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	if _, ok, _ := e.svc.UserByToken(e.ctx, plain); ok {
		t.Fatal("cambiar la contraseña debe revocar los tokens")
	}
	if _, ok, _ := e.svc.Authenticate(e.ctx, "ana", "contraseña-larga"); ok {
		t.Fatal("la contraseña anterior no debe valer")
	}
	if _, ok, _ := e.svc.Authenticate(e.ctx, "ana", "otra-contraseña-larga"); !ok {
		t.Fatal("la contraseña nueva debe valer")
	}
	wantRule(t, e.svc.SetPassword(e.ctx, e.admin.UserID, 999, "otra-contraseña-larga"), rules.CodeNotFound)
}
