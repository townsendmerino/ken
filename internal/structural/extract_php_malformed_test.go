package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_PhpTruncatedController is a GRAMMAR-BUMP GUARD, not a feature
// test. It feeds a truncated Yii-style controller (cut off mid-expression
// inside the second method) through Build and pins the error-recovery SHAPE
// that gotreesitter 0.47.0 produces. Its job is to make a future grammar
// bump's error-tree changes VISIBLE in CI rather than silent.
//
// Why this exists: the 256.6 s external bench (Trofimov, 2026-07-18) ran ken
// v1.1.x on gotreesitter 0.20.5, whose line silently disabled the
// hand-written repeat-boundary conflict resolvers for PHP (0.21.0 changelog),
// causing GLR blowups on real code. We bumped to 0.47.0 (PR #62, 2026-07-24),
// which switched to the C-faithful engine and changed error-tree shapes for
// elected languages (e.g. PHP static named functions are now explicitly
// named). Before that bump, ken had NO malformed-input fixture for any
// language — every extractor test used deliberately well-formed source — so a
// shape change could ride in unnoticed. This closes that gap for PHP, the
// language the bench exercised.
//
// If this test's assertions ever diff after a gotreesitter bump: DO NOT blindly
// re-baseline. Confirm each change matches the new version's changelog
// (C-oracle error-tree shapes) first, then update the pinned shape here and
// note the verification in the commit message. See docs/internal/cold-start-campaign.md
// (Task 2 / golden-drift risk).
func TestBuild_PhpTruncatedController(t *testing.T) {
	dir := t.TempDir()
	// A realistic Yii controller truncated mid-expression in actionLogin:
	// the `if (...)` condition is cut off and the class body brace never
	// closes. This is what a partially-typed / partially-saved file looks
	// like to the indexer.
	src := `<?php
namespace app\controllers;

use yii\web\Controller;
use yii\web\Response;

class SiteController extends Controller
{
    public function actionIndex()
    {
        return $this->render('index');
    }

    public function actionLogin()
    {
        $model = new LoginForm();
        if ($model->load(Yii::$app->request->post()) && $model->`
	if err := os.WriteFile(filepath.Join(dir, "SiteController.php"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// Robustness invariant (the load-bearing one): a truncated file must
	// never crash the indexer. Build recovering — not panicking or erroring
	// — is the guarantee the bench's PHP corpus depended on.
	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build must not error on truncated input: %v", err)
	}
	fs := ix.File("SiteController.php")
	if fs == nil {
		t.Fatal("truncated SiteController.php was not indexed; error recovery regressed " +
			"(0.47.0 parse-accepts it with recovery — a shape change worth verifying)")
	}

	// --- Pinned gotreesitter 0.47.0 error-recovery shape ---------------
	// A COMPLETE named method before the truncation is recovered and named.
	if !funcNamesContain(fs.Functions, "actionIndex") {
		t.Errorf("expected the complete method actionIndex to be recovered; got %v", funcNames(fs.Functions))
	}
	// The truncated method (actionLogin) is NOT recovered as a function at
	// 0.47.0 — its body is inside the unterminated construct. If a future
	// bump starts naming it, that is a shape change to verify, not accept.
	if funcNamesContain(fs.Functions, "actionLogin") {
		t.Errorf("actionLogin was recovered as a function — gotreesitter error-recovery shape CHANGED; "+
			"verify against the changelog before re-baselining. funcs=%v", funcNames(fs.Functions))
	}
	// The class node's body brace never closes → ERROR node → no class is
	// extracted at 0.47.0. A future bump that recovers the class is a shape
	// change to verify.
	if len(fs.Classes) != 0 {
		t.Errorf("expected no class from the unterminated class body at 0.47.0; got %+v "+
			"(shape change — verify against changelog)", fs.Classes)
	}
	// Imports parse cleanly (they precede the truncation): rightmost-segment
	// binding of `use yii\web\Controller`.
	if !contains(fs.Imports, "Controller") {
		t.Errorf("expected Controller import; got %v", fs.Imports)
	}
}

// funcNamesContain reports whether any extracted function has the given name.
func funcNamesContain(fns []FuncDef, name string) bool {
	for _, fn := range fns {
		if fn.Name == name {
			return true
		}
	}
	return false
}
