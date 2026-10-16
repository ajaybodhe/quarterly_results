package logging

import (
	"fmt"
	"os"
)

func Logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}
