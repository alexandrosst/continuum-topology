package server

import "continuum/internal/install"

// Everything below builds text a person pastes into a shell. A value that came from anywhere but a constant (an
// address, a chart reference an organisation configured, a host from the request) is quoted, so it can only ever
// be one word of the command, and a private key is never an argument (arguments are visible to every process on the
// machine and land in shell history): Secrets are applied from a manifest on standard input.

// shellArg is s as one shell word, quoted only when it has to be. The rule lives in internal/install, which is where commands are
// written; these two names remain for the builders in this package that have not moved there yet.
func shellArg(s string) string { return install.Arg(s) }
