package main

import (
	"fmt"
	"time"

	"golin/cmd"
)

func main() {
	start := time.Now()
	cmd.Execute()
	fmt.Printf("[*] 任务结束,耗时: %s\n", time.Since(start))
}
