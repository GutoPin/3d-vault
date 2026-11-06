package agent

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	tmpDirName = ".3dvault-tmp"
	ignoreFile = ".3dvaultignore"
)

// defaultIgnores son archivos de trabajo de Blender y del sistema, además de lo que
// genera el propio agente.
var defaultIgnores = []string{
	"*.blend[0-9]*",
	"*.blend@",
	"*.conflicto-*",
	"*.tmp",
	"~*",
	".DS_Store",
	"Thumbs.db",
	"desktop.ini",
	".3dvault-tmp-*",
}

// ignoreRules decide qué archivos no se sincronizan.
type ignoreRules struct {
	patterns []string
}

// loadIgnoreRules lee .3dvaultignore en la raíz del proyecto. Si el archivo está
// dañado se usa lo que se pudo leer: ignorar de menos solo hace que aparezcan más
// archivos en la lista de nuevos, y subirlos sigue siendo una decisión del usuario.
func loadIgnoreRules(projectDir string) ignoreRules {
	rules := ignoreRules{patterns: append([]string{}, defaultIgnores...)}
	f, err := os.Open(filepath.Join(projectDir, ignoreFile))
	if err != nil {
		return rules
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules.patterns = append(rules.patterns, strings.TrimSuffix(strings.TrimPrefix(line, "/"), "/"))
	}
	return rules
}

// Match indica si una ruta relativa, con "/" como separador, se ignora. Un patrón
// sin "/" se compara con cada parte de la ruta, de modo que también excluye carpetas;
// un patrón con "/" se compara con la ruta entera. Un patrón mal formado no coincide.
func (r ignoreRules) Match(relPath string) bool {
	segments := strings.Split(relPath, "/")
	for _, pattern := range r.patterns {
		if strings.Contains(pattern, "/") {
			if matchFold(pattern, relPath) {
				return true
			}
			continue
		}
		for _, segment := range segments {
			if matchFold(pattern, segment) {
				return true
			}
		}
	}
	return false
}

func matchFold(pattern, name string) bool {
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(name))
	return err == nil && ok
}

// walkProject recorre la carpeta del proyecto saltando lo ignorado. Llama a onFile
// por cada archivo y a onDir por cada carpeta.
func walkProject(dir string, rules ignoreRules, onDir func(string), onFile func(rel string, info fs.FileInfo)) error {
	return filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			// Algo desapareció o no se puede leer: se sigue con lo demás.
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return err
		}
		if p == dir {
			onDir(p)
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if entry.IsDir() {
			if entry.Name() == tmpDirName || rules.Match(rel) {
				return filepath.SkipDir
			}
			onDir(p)
			return nil
		}
		// Los enlaces simbólicos no se siguen: podrían sacar del proyecto.
		if !entry.Type().IsRegular() || entry.Name() == ignoreFile || rules.Match(rel) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		onFile(rel, info)
		return nil
	})
}
