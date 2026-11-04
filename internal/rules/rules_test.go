package rules

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var (
	juanPC1 = Actor{UserID: 1, ClientID: "pc-1", ClientName: "PC de Juan"}
	juanPC2 = Actor{UserID: 1, ClientID: "pc-2", ClientName: "Portátil de Juan"}
	ana     = Actor{UserID: 2, ClientID: "pc-3", ClientName: "PC de Ana"}
	admin   = Actor{UserID: 3, IsAdmin: true, ClientID: "pc-4", ClientName: "PC admin"}

	juanLock = &Lock{
		UserID:      1,
		Username:    "juan",
		DisplayName: "Juan",
		ClientID:    "pc-1",
		ClientName:  "PC de Juan",
		LockedAt:    time.Date(2026, 11, 4, 15, 3, 0, 0, time.UTC),
	}
)

func wantCode(t *testing.T, err error, want Code) {
	t.Helper()
	var ruleErr *Error
	if want == "" {
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		return
	}
	if !errors.As(err, &ruleErr) {
		t.Fatalf("error = %v, quiero código %q", err, want)
	}
	if ruleErr.Code != want {
		t.Fatalf("código = %q, quiero %q", ruleErr.Code, want)
	}
}

// Regla 3: comentario obligatorio de al menos 3 caracteres.
func TestCheckComment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
		code  Code
	}{
		{"vacío", "", "", CodeInvalidComment},
		{"solo espacios", "     ", "", CodeInvalidComment},
		{"dos caracteres", "ok", "", CodeInvalidComment},
		{"tres caracteres", "fix", "fix", ""},
		{"cuenta caracteres, no bytes", "ñáé", "ñáé", ""},
		{"dos caracteres con acento", "ñá", "", CodeInvalidComment},
		{"recorta espacios", "  ajuste de ruedas  ", "ajuste de ruedas", ""},
		{"demasiado largo", strings.Repeat("a", 2001), "", CodeInvalidComment},
		{"en el límite", strings.Repeat("a", 2000), strings.Repeat("a", 2000), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CheckComment(tt.input)
			wantCode(t, err, tt.code)
			if got != tt.want {
				t.Fatalf("comentario = %q, quiero %q", got, tt.want)
			}
		})
	}
}

// Regla 2: subir, restaurar, mover y borrar según bloqueo y versión de partida.
func TestCheckWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		actor Actor
		lock  *Lock
		base  int64
		code  Code
	}{
		{"sin bloqueo", ana, nil, 7, ""},
		{"bloqueado por mí en esta PC", juanPC1, juanLock, 7, ""},
		{"bloqueado por otro", ana, juanLock, 7, CodeLocked},
		{"bloqueado por mí en otra PC", juanPC2, juanLock, 7, CodeLockedOtherClient},
		{"un admin tampoco puede pasar por encima", admin, juanLock, 7, CodeLocked},
		{"versión vieja sin bloqueo", ana, nil, 6, CodeStaleVersion},
		{"versión vieja con bloqueo propio", juanPC1, juanLock, 6, CodeStaleVersion},
		{"versión de partida ausente", ana, nil, 0, CodeStaleVersion},
		{"el bloqueo tiene prioridad sobre la versión", ana, juanLock, 6, CodeLocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wantCode(t, CheckWrite(tt.actor, tt.lock, 7, tt.base), tt.code)
		})
	}
}

func TestCheckWriteMessageNamesHolderAndDate(t *testing.T) {
	t.Parallel()

	err := CheckWrite(ana, juanLock, 7, 7)
	want := "Bloqueado por Juan desde 04/11/2026 15:03."
	if err == nil || err.Error() != want {
		t.Fatalf("mensaje = %v, quiero %q", err, want)
	}
	var ruleErr *Error
	if !errors.As(err, &ruleErr) || ruleErr.HeadVersion != 7 || ruleErr.Lock != juanLock {
		t.Fatalf("el error debe incluir la versión y el bloqueo: %+v", ruleErr)
	}
}

func TestCheckAcquire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		actor    Actor
		lock     *Lock
		takeover bool
		want     LockDecision
		code     Code
	}{
		{"libre", ana, nil, false, LockAcquire, ""},
		{"ya es mío en esta PC", juanPC1, juanLock, false, LockKeep, ""},
		{"mío en otra PC, sin pedirlo", juanPC2, juanLock, false, 0, CodeLockedOtherClient},
		{"mío en otra PC, pidiéndolo", juanPC2, juanLock, true, LockTakeover, ""},
		{"de otro", ana, juanLock, false, 0, CodeLocked},
		{"no se puede tomar el de otro", ana, juanLock, true, 0, CodeLocked},
		{"un admin no lo toma, lo fuerza", admin, juanLock, true, 0, CodeLocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CheckAcquire(tt.actor, tt.lock, 7, tt.takeover)
			wantCode(t, err, tt.code)
			if got != tt.want {
				t.Fatalf("decisión = %d, quiero %d", got, tt.want)
			}
		})
	}
}

// Regla 4: solo quien tiene el bloqueo lo libera; el admin puede forzarlo.
func TestCheckRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		actor      Actor
		lock       *Lock
		force      bool
		code       Code
		wantForced bool
	}{
		{"libre", ana, nil, false, "", false},
		{"propio", juanPC1, juanLock, false, "", false},
		{"propio desde otra PC", juanPC2, juanLock, false, "", false},
		{"de otro", ana, juanLock, false, CodeNotLockOwner, true},
		{"forzar sin ser admin", ana, juanLock, true, CodeForbidden, true},
		{"admin sin pedir forzar", admin, juanLock, false, CodeNotLockOwner, true},
		{"admin forzando", admin, juanLock, true, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wantCode(t, CheckRelease(tt.actor, tt.lock, tt.force), tt.code)
			if got := IsForcedRelease(tt.actor, tt.lock); got != tt.wantForced {
				t.Fatalf("IsForcedRelease = %v, quiero %v", got, tt.wantForced)
			}
		})
	}
}

// Regla 6: mismo contenido, sin versión nueva.
func TestSameContent(t *testing.T) {
	t.Parallel()

	if !SameContent("abc", "abc") {
		t.Error("el mismo hash debe ser mismo contenido")
	}
	if SameContent("abc", "abd") {
		t.Error("hashes distintos no son mismo contenido")
	}
}

// Regla 7: la extensión no cambia.
func TestCheckExtension(t *testing.T) {
	t.Parallel()

	wantCode(t, CheckExtension(".blend", ".blend"), "")
	wantCode(t, CheckExtension(".blend", ".BLEND"), "")
	wantCode(t, CheckExtension(".blend", ".fbx"), CodeExtensionChanged)
	wantCode(t, CheckExtension(".blend", ""), CodeExtensionChanged)
}

// Regla 5: comentario de una restauración.
func TestRestoreComment(t *testing.T) {
	t.Parallel()

	if got := RestoreComment(3); got != "Restaurado desde v3" {
		t.Fatalf("RestoreComment = %q", got)
	}
}

func TestNormalizePathAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		clean string
		key   string
		ext   string
	}{
		{"simple", "camion.blend", "camion.blend", "camion.blend", ".blend"},
		{"con carpetas", "modelos/camiones/camion_797F.blend", "modelos/camiones/camion_797F.blend", "modelos/camiones/camion_797f.blend", ".blend"},
		{"sin extensión", "LEEME", "LEEME", "leeme", ""},
		{"extensión en mayúsculas", "A.BLEND", "A.BLEND", "a.blend", ".blend"},
		{"espacios internos", "mi camión.blend", "mi camión.blend", "mi camión.blend", ".blend"},
		{"oculto", ".3dvaultignore", ".3dvaultignore", ".3dvaultignore", ".3dvaultignore"},
		{"parecido a reservado", "console.blend", "console.blend", "console.blend", ".blend"},
		{"COM sin número", "com.blend", "com.blend", "com.blend", ".blend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizePath(tt.input)
			if err != nil {
				t.Fatalf("NormalizePath: %v", err)
			}
			if got.Clean != tt.clean || got.Key != tt.key || got.Ext != tt.ext {
				t.Fatalf("resultado = %+v", got)
			}
		})
	}
}

func TestNormalizePathRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"vacía", ""},
		{"absoluta", "/etc/passwd"},
		{"subir de carpeta", "../secreto.blend"},
		{"subir en medio", "modelos/../../secreto.blend"},
		{"punto", "./camion.blend"},
		{"barra final", "modelos/"},
		{"doble barra", "modelos//camion.blend"},
		{"barra invertida", `modelos\camion.blend`},
		{"unidad de Windows", "C:/camion.blend"},
		{"punto final", "camion.blend."},
		{"espacio final", "camion.blend "},
		{"carpeta con punto final", "modelos./camion.blend"},
		{"reservado", "CON"},
		{"reservado con extensión", "nul.blend"},
		{"reservado en carpeta", "aux/camion.blend"},
		{"puerto serie", "COM1.blend"},
		{"impresora", "lpt9.txt"},
		{"carácter de control", "cami\x01on.blend"},
		{"salto de línea", "cami\non.blend"},
		{"asterisco", "camion*.blend"},
		{"interrogación", "camion?.blend"},
		{"comillas", `camion".blend`},
		{"menor que", "camion<.blend"},
		{"tubería", "camion|.blend"},
		{"inversión bidireccional", "camion\u202e.blend"},
		{"espacio de ancho cero", "camion\u200b.blend"},
		{"UTF-8 no válido", "camion\xff.blend"},
		{"segmento muy largo", strings.Repeat("a", 256)},
		{"ruta muy larga", strings.Repeat("carpeta/", 130) + "a.blend"},
		{"carpeta del agente", ".3dvault-tmp/a.blend"},
		{"carpeta del agente en mayúsculas", "modelos/.3DVAULT-TMP/a.blend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NormalizePath(tt.input)
			wantCode(t, err, CodeInvalidPath)
		})
	}
}

func TestNormalizePathKeysDetectCollisions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
	}{
		{"mayúsculas", "Camion.blend", "camion.blend"},
		{"carpeta en mayúsculas", "MODELOS/a.blend", "modelos/a.blend"},
		{"NFC y NFD", "cami\u00f3n.blend", "camio\u0301n.blend"},
		{"NFD con mayúsculas", "CAMI\u00d3N.blend", "camio\u0301n.blend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, err := NormalizePath(tt.a)
			if err != nil {
				t.Fatal(err)
			}
			b, err := NormalizePath(tt.b)
			if err != nil {
				t.Fatal(err)
			}
			if a.Key != b.Key {
				t.Fatalf("claves distintas: %q y %q", a.Key, b.Key)
			}
		})
	}
}

// FuzzNormalizePath comprueba que ninguna ruta aceptada pueda salir de la carpeta
// del proyecto y que normalizar dos veces no cambie el resultado.
func FuzzNormalizePath(f *testing.F) {
	for _, seed := range []string{"a.blend", "../x", "a/b/c.fbx", `a\b`, "C:/x", "camio\u0301n", "\x00", "CON.txt", "a/./b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got, err := NormalizePath(input)
		if err != nil {
			return
		}
		if !utf8.ValidString(got.Clean) {
			t.Fatalf("ruta no válida aceptada: %q", got.Clean)
		}
		if strings.HasPrefix(got.Clean, "/") || strings.Contains(got.Clean, `\`) || strings.Contains(got.Clean, ":") {
			t.Fatalf("ruta peligrosa aceptada: %q", got.Clean)
		}
		for segment := range strings.SplitSeq(got.Clean, "/") {
			if segment == "" || segment == "." || segment == ".." {
				t.Fatalf("segmento peligroso aceptado: %q", got.Clean)
			}
		}
		again, err := NormalizePath(got.Clean)
		if err != nil {
			t.Fatalf("rechazó su propia salida %q: %v", got.Clean, err)
		}
		if again != got {
			t.Fatalf("no es estable: %+v y %+v", got, again)
		}
	})
}
