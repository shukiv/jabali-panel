// support-claim: standalone operator-run claim/redeem service for Jabali
// diagnostic bundles. Stdlib-only; its own module so it builds + deploys
// independently of the panel (the root go.mod's `go build ./...` ignores it).
module git.jabali-panel.com/shukivaknin/jabali2/support-claim

go 1.24
