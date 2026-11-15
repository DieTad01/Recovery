//go:build !windows

package main

import "fmt"

func listPrograms(progress func(done, total int)) ([]Program, error) {
	return nil, fmt.Errorf("la lista de programas instalados solo está disponible en Windows")
}
