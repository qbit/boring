package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	t, _ := time.Parse("2006-01-02", os.Args[1])
	fmt.Println(t.Format(time.RFC1123))
}
