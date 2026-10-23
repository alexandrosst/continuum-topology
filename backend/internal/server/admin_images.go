package server

import "ikhnos/internal/kernel"

// The image settings the server was started with, and how an organisation's own settings take precedence over them. The shape of
// the configuration and its checks are images.go's; what the flags say is the shell's.

// images is the image configuration an organisation's install commands use. Precedence: the organisation's own
// setting (Settings → Installation), then the server's --image-* flags, then nothing (the chart's built-in names).
// The three parts are taken together from one source, so a digest saved for a registry is never combined with a
// different registry from a flag.
func (a *Admin) images(c *Core) kernel.ImageConfig {
	if c == nil {
		return a.ImageDefaults()
	}
	return a.Images(c.Core)
}
