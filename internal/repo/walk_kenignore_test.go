package repo

import (
	"reflect"
	"sort"
	"testing"
	"testing/fstest"
)

// TestWalkFS_KenIgnore exercises ADR-038: the .kenignore / .sembleignore
// ignore family, layered as an independent union on top of .gitignore.
// Each subtest builds a minimal fstest.MapFS, runs WalkFS, and asserts the
// indexed file list. Note the ignore files themselves are regular files and
// are indexed (same as .gitignore in the ADR-015 tests).
func TestWalkFS_KenIgnore(t *testing.T) {
	run := func(t *testing.T, fsys fstest.MapFS, want []string) {
		t.Helper()
		got, err := WalkFS(fsys, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v", got, want)
		}
	}

	t.Run("kenignore alone prunes a built-asset dir", func(t *testing.T) {
		// The Denis/Yii case in miniature: a built-asset dir the repo
		// keeps in git (so .gitignore won't exclude it) but the user
		// doesn't want ken to index.
		run(t, fstest.MapFS{
			".kenignore":      {Data: []byte("build/\n")},
			"build/bundle.js": {Data: []byte("/*built*/\n")},
			"src/main.go":     {Data: []byte("package main\n")},
		}, []string{".kenignore", "src/main.go"})
	})

	t.Run("sembleignore is the fallback when no kenignore", func(t *testing.T) {
		run(t, fstest.MapFS{
			".sembleignore":  {Data: []byte("vendor/\n")},
			"vendor/lib.php": {Data: []byte("<?php\n")},
			"src/app.php":    {Data: []byte("<?php\n")},
		}, []string{".sembleignore", "src/app.php"})
	})

	t.Run("kenignore wins over sembleignore (sembleignore not loaded)", func(t *testing.T) {
		// .kenignore present → .sembleignore rules are NOT consulted, so
		// src/ is kept even though .sembleignore would have excluded it.
		run(t, fstest.MapFS{
			".kenignore":    {Data: []byte("build/\n")},
			".sembleignore": {Data: []byte("src/\n")},
			"build/x.js":    {Data: []byte("x\n")},
			"src/main.go":   {Data: []byte("package main\n")},
		}, []string{".kenignore", ".sembleignore", "src/main.go"})
	})

	t.Run("empty kenignore still suppresses sembleignore (existence wins)", func(t *testing.T) {
		run(t, fstest.MapFS{
			".kenignore":    {Data: []byte("")}, // exists but no rules
			".sembleignore": {Data: []byte("src/\n")},
			"src/main.go":   {Data: []byte("package main\n")},
		}, []string{".kenignore", ".sembleignore", "src/main.go"})
	})

	t.Run("union: gitignore and kenignore both prune", func(t *testing.T) {
		run(t, fstest.MapFS{
			".gitignore":   {Data: []byte("build/\n")},
			".kenignore":   {Data: []byte("vendor/\n")},
			"build/x.js":   {Data: []byte("x\n")},
			"vendor/y.php": {Data: []byte("<?php\n")},
			"src/main.go":  {Data: []byte("package main\n")},
		}, []string{".gitignore", ".kenignore", "src/main.go"})
	})

	t.Run("no cross-file re-include: kenignore negation cannot resurrect a git-ignored path", func(t *testing.T) {
		// git family ignores secret.txt; a !secret.txt in .kenignore
		// updates only the ken decision, so the union stays ignored.
		run(t, fstest.MapFS{
			".gitignore": {Data: []byte("secret.txt\n")},
			".kenignore": {Data: []byte("!secret.txt\n")},
			"secret.txt": {Data: []byte("shh\n")},
			"main.go":    {Data: []byte("package main\n")},
		}, []string{".gitignore", ".kenignore", "main.go"})
	})

	t.Run("no cross-file re-include: gitignore negation cannot resurrect a ken-ignored path", func(t *testing.T) {
		run(t, fstest.MapFS{
			".kenignore": {Data: []byte("secret.txt\n")},
			".gitignore": {Data: []byte("!secret.txt\n")},
			"secret.txt": {Data: []byte("shh\n")},
			"main.go":    {Data: []byte("package main\n")},
		}, []string{".gitignore", ".kenignore", "main.go"})
	})

	t.Run("within-family negation subset works (re-include one file)", func(t *testing.T) {
		// *.log then !keep.log in the SAME family composes last-match-wins,
		// exactly like stock gitignore — this is within-family, not cross.
		run(t, fstest.MapFS{
			".kenignore": {Data: []byte("*.log\n!keep.log\n")},
			"drop.log":   {Data: []byte("noise\n")},
			"keep.log":   {Data: []byte("kept\n")},
			"main.go":    {Data: []byte("package main\n")},
		}, []string{".kenignore", "keep.log", "main.go"})
	})

	t.Run("nested kenignore composes with the root ken scope", func(t *testing.T) {
		run(t, fstest.MapFS{
			".kenignore":     {Data: []byte("*.tmp\n")},
			"pkg/.kenignore": {Data: []byte("*.cache\n")},
			"root.tmp":       {Data: []byte("x\n")},
			"pkg/a.tmp":      {Data: []byte("x\n")}, // root scope
			"pkg/b.cache":    {Data: []byte("x\n")}, // nested scope
			"pkg/main.go":    {Data: []byte("package main\n")},
		}, []string{".kenignore", "pkg/.kenignore", "pkg/main.go"})
	})
}
