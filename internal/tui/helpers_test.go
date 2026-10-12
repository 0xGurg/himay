package tui

import (
	"fmt"
	"strconv"
)

func fmtType(v any) string { return fmt.Sprintf("%T", v) }
func fmtInt(n int) string  { return strconv.Itoa(n) }
