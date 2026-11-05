//go:build !unix

package php

import (
	"context"
	"errors"
	"testing"
)

func TestNoPoolOnThisOS(t *testing.T) {
	m, _ := testManager(t, fixtureManifest(t))
	_, err := m.PoolSpec(DefaultVersion)
	if !errors.Is(err, ErrNoPool) {
		t.Fatalf("PoolSpec error = %v", err)
	}
	svcs, err := m.Services(context.Background(), []string{DefaultVersion})
	if err != nil || svcs != nil {
		t.Fatalf("services = %+v, %v", svcs, err)
	}
	if _, err := m.Services(context.Background(), []string{"9.9"}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown error = %v", err)
	}
	res, err := m.Install(context.Background(), DefaultVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.CLI != "" || res.FPM != "" {
		t.Fatalf("windows install reported a unix archive: %+v", res)
	}
	svcs, err = m.Services(context.Background(), []string{DefaultVersion})
	if err != nil || svcs != nil {
		t.Fatalf("services after install = %+v, %v", svcs, err)
	}
}
