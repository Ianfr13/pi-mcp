package runner

import "pi-mcp/internal/config"

// BuildArgv assembles the full argv for the pi subprocess (§4):
//
//	pi -p --mode json --no-session [--no-context-files] <single positional prompt>
//
// argv[0] is config.PiBinary; the prompt is appended as exactly ONE element
// (no shell, no escaping; newlines preserved). When noContextFiles is true the
// config.NoContextFilesFlag is inserted before the positional prompt.
func BuildArgv(prompt string, noContextFiles bool) []string {
	argv := make([]string, 0, len(config.PiBaseFlags)+3)
	argv = append(argv, config.PiBinary)
	argv = append(argv, config.PiBaseFlags...)
	if noContextFiles {
		argv = append(argv, config.NoContextFilesFlag)
	}
	argv = append(argv, prompt)
	return argv
}
