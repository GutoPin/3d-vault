package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// readPassword pide la contraseña sin mostrarla, o la lee de la entrada estándar
// si no hay terminal, por ejemplo al usarlo desde un script.
func readPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("leer la contraseña: %w", err)
		}
		return string(raw), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no se recibió ninguna contraseña por la entrada estándar")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
