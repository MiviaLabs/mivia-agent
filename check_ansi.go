package main

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
)

func main() {
	s1 := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	s2 := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	s3 := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	s4 := lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	fmt.Printf("color 8:  %#v\n", s1)
	fmt.Printf("color 243: %#v\n", s2)
	fmt.Printf("color 14: %#v\n", s3)
	fmt.Printf("color 12: %#v\n", s4)

	// With color profile
	l2 := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "8", Dark: "8"}).
		Render("test")
	fmt.Printf("adaptive: %#v\n", []byte(l2))

	l3 := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "243", Dark: "243"}).
		Render("test")
	fmt.Printf("adaptive243: %#v\n", []byte(l3))
}
