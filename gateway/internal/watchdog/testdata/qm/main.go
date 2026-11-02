package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(1)
	}
	switch os.Args[1] {
	case "status":
		fmt.Println("status: running")
	case "listsnapshot":
		fmt.Println("current")
	case "agent":
		fmt.Println("thawed")
	case "guest":
		fmt.Println(`{"exited":1,"exitcode":1}`)
	default:
		os.Exit(1)
	}
}
