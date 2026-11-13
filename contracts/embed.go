// Package contracts embeds the move contracts and pressures. The architect and the twin load
// the same files, so a contract change is tested across seeds before any playground starts.
package contracts

import "embed"

//go:embed *.yaml
var FS embed.FS
