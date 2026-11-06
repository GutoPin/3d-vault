// Package web contiene las plantillas y los archivos estáticos de la web, que van
// incluidos en el binario.
package web

import "embed"

// Files contiene server/templates y server/static.
//
//go:embed server
var Files embed.FS

// BlenderDir es el complemento de Blender. "all:" incluye __init__.py, que
// empieza por guion bajo.
const BlenderDir = "blender/three_d_vault"

// Blender contiene el complemento de Blender que sirve /blender/3dvault.zip.
//
//go:embed all:blender/three_d_vault
var Blender embed.FS
