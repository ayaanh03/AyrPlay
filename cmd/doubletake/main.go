// Command doubletake is the command-line AirPlay sender. AyrPlay ships the same
// sender inside its single AyrPlay executable (cmd/ayrplay); this binary keeps
// the upstream name for Linux packaging and scripts.
package main

import (
	"os"
	"path/filepath"

	"doubletake/internal/sender"
)

func main() {
	sender.Main(filepath.Base(os.Args[0]), os.Args[1:])
}
