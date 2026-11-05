package daemon

import (
	"strings"
	"testing"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/php"
)

func TestPHPRemoveAndDefault(t *testing.T) {
	home, addr := testEnv(t)
	inst := start(t, home, addr, nil)
	ctx := testCtx(t)

	err := inst.client.RemovePHP(ctx, php.DefaultVersion)
	var ae *api.Error
	if !errorAs(err, &ae) || ae.Code != api.CodeConflict || !strings.Contains(ae.Message, "default") {
		t.Fatalf("remove default = %v", err)
	}
	err = inst.client.SetDefaultPHP(ctx, "8.1")
	if !errorAs(err, &ae) || ae.Code != api.CodeNotFound {
		t.Fatalf("default uninstalled = %v", err)
	}
	err = inst.client.SetDefaultPHP(ctx, "nope")
	if !errorAs(err, &ae) || ae.Code != api.CodeBadRequest {
		t.Fatalf("default bad = %v", err)
	}

	root := t.TempDir()
	if err := inst.client.Park(ctx, root); err != nil {
		t.Fatal(err)
	}
	phpVersion := "8.1"
	if _, err := inst.client.Link(ctx, api.LinkRequest{Path: root, Name: "isolated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.client.PatchSite(ctx, "isolated", api.SitePatch{PHP: &phpVersion}); err != nil {
		t.Fatal(err)
	}
	err = inst.client.RemovePHP(ctx, "8.1")
	if !errorAs(err, &ae) || ae.Code != api.CodeConflict || !strings.Contains(ae.Hint, "unisolate") {
		t.Fatalf("remove isolated = %v", err)
	}
}

func errorAs(err error, target **api.Error) bool {
	if err == nil {
		return false
	}
	ae, ok := err.(*api.Error)
	if !ok {
		return false
	}
	*target = ae
	return true
}
