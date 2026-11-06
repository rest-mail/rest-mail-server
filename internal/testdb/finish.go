package testdb

import (
	"fmt"
	"os"
)

func Finish(code int) int {
	if err := CloseTemplates(); err != nil {
		fmt.Fprintln(os.Stderr, "test database templates:", err)
		if code == 0 {
			return 1
		}
	}
	return code
}
