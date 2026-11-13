// Package k8s embeds the manifests the architect renders and applies.
package k8s

import "embed"

//go:embed *.yaml
var FS embed.FS
