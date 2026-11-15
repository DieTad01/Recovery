package main

import (
	"fmt"
	"strings"
)

// reporter recibe la salida del motor de recuperación. Hay dos: la consola
// (texto) y la interfaz gráfica de Windows (ventana). Así el mismo código
// de recuperación sirve para los dos modos.
type reporter interface {
	line(s string)         // una línea permanente del informe
	status(s string)       // línea de progreso que se sobrescribe
	progress(frac float64) // avance 0..1 (negativo = sin barra)
}

// out es el destino actual de la salida. Por defecto, la consola.
var out reporter = &consoleReporter{}

// consoleReporter escribe en la terminal.
type consoleReporter struct {
	lastLen int
}

func (c *consoleReporter) line(s string) { fmt.Println(s) }

func (c *consoleReporter) status(s string) {
	if r := []rune(s); len(r) > 118 {
		s = string(r[:118])
	}
	n := len([]rune(s))
	pad := ""
	if n < c.lastLen {
		pad = strings.Repeat(" ", c.lastLen-n)
	}
	fmt.Print("\r" + s + pad)
	if s == "" {
		fmt.Print("\r")
	}
	c.lastLen = n
}

func (c *consoleReporter) progress(float64) {} // la consola ya muestra el % en el texto
