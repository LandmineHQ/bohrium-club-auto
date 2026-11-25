package web

import "embed"

// FS embeds the compiled Vite/React admin dashboard assets from web/dist into the single Go binary.
// web/dist/.gitkeep is tracked so Go packages compile and unit tests pass even before running `npm run build --prefix web`.
//
//go:embed all:dist
var FS embed.FS
