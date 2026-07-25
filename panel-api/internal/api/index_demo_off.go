//go:build !demo

package api

import "github.com/gin-gonic/gin"

// injectDemo is a no-op in production builds: the demo credential block does not
// exist in the binary. The demo variant (index_demo_on.go, //go:build demo)
// carries the DemoInfo type + the /info exposer. This is the security boundary
// for JAB-159 — a prod binary can never surface seeded demo credentials.
func injectDemo(_ gin.H) {}
