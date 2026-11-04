// Package web contiene las plantillas y los archivos estáticos de la web, que van
// incluidos en el binario.
package web

import "embed"

// Files contiene server/templates y server/static.
//
//go:embed server
var Files embed.FS
