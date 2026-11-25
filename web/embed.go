package web

import "embed"

// FS embeds the compiled/static admin dashboard assets into the single Go binary.
//
//go:embed static/*
var FS embed.FS
