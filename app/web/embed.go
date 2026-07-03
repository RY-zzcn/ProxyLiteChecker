package webassets

import "embed"

// FS embeds the production web console so release binaries can run without
// requiring a sibling app/web directory.
//
//go:embed index.html favicon.svg static/*
var FS embed.FS
