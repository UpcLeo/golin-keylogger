//go:build !windows

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func KeyLoggerCmd(_ *cobra.Command, _ []string) {
	fmt.Printf("[-] 仅支持Windows操作系统\n")
	return
}
