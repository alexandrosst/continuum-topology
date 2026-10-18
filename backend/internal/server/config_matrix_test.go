package server

import (
	"flag"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files")
