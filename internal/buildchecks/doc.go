// Package buildchecks holds drift-guard tests that wire together ken's
// release configuration (.goreleaser.yml at the repo root) with package
// state that lives in the aikit module. The test moved here from
// aikit/chunk/treesitter/ during the M0 → aikit extraction because
// .goreleaser.yml is a KEN release artifact, not an aikit one.
package buildchecks
