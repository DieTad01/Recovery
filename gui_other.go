//go:build !windows

package main

// En sistemas que no son Windows no hay interfaz gráfica: el programa
// funciona por consola. Estas funciones solo existen para que compile.

func runGUIApp(cfg *Config) int { return 0 }

func attachConsole() {}
