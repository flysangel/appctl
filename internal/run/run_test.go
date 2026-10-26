package run

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestExecMissingCommandIsErrNotFound(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), "appctl-no-such-command-xyz", nil, nil)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("要能用 errors.Is 認出 exec.ErrNotFound，得到：%v", err)
	}
}
