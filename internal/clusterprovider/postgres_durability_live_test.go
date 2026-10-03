//go:build linux

package clusterprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
)

func durabilitySpec(t *testing.T, cluster string) PostgresSpec {
	t.Helper()
	skipWithoutPostgres(t)
	alloc := testAllocatorIn(t, durabilityRange)
	lease, err := alloc.Allocate(portallocator.Request{Cluster: cluster, Purpose: portallocator.PurposePostgres})
	if err != nil {
		t.Fatalf("allocate PurposePostgres: %v", err)
	}
	return PostgresSpec{Cluster: cluster, StateDir: t.TempDir(), Port: lease.Port, Log: discard()}
}

func TestTheStoreSurvivesAScaleToZeroAndReprovision(t *testing.T) {
	spec := durabilitySpec(t, "scalezero")

	first, err := StartPostgres(context.Background(), spec)
	if err != nil {
		t.Fatalf("StartPostgres: %v", err)
	}
	dsn, password := first.DSN(), first.password
	sql(t, first, `CREATE TABLE IF NOT EXISTS survive (k text primary key, v text)`)
	sql(t, first, `INSERT INTO survive VALUES ('object','still here')`)
	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if first.Up() {
		t.Fatal("Up() is true after Stop, so the predicate would pass a store that is gone")
	}

	second, err := StartPostgres(context.Background(), spec)
	if err != nil {
		t.Fatalf("the re-provision did not start the store again on %s: %v", spec.StateDir, err)
	}
	defer func() {
		if err := second.Stop(context.Background()); err != nil {
			t.Errorf("CLEANUP: %v", err)
		}
	}()

	if got := sql(t, second, `SELECT v FROM survive WHERE k='object'`); got != "still here" {
		t.Fatalf("the object written before the store was stopped reads %q after the re-provision: "+
			"a cluster that scales to zero and comes back would come back empty, which is the defect "+
			"this store exists to close", got)
	}
	if second.DSN() != dsn || second.password != password {
		t.Fatalf("the DSN a node is handed changed across the cycle:\n was: %s\n now: %s",
			dsn, second.DSN())
	}
	if got := filepath.Clean(sql(t, second, "show data_directory")); got != filepath.Clean(spec.DataDir()) {
		t.Fatalf("the re-provisioned store serves %s, this cluster's store is %s: a store that is "+
			"not this cluster's says nothing about this cluster's data", got, spec.DataDir())
	}
	if got := sql(t, second, "select count(*) from pg_database where datname='"+PostgresDatabase+"'"); got != "1" {
		t.Fatalf("%s database count=%q after the re-provision", PostgresDatabase, got)
	}
}

func TestTheStoreIsLostWhenTheDataDirIsRemoved(t *testing.T) {
	spec := durabilitySpec(t, "datadirgone")

	first, err := StartPostgres(context.Background(), spec)
	if err != nil {
		t.Fatalf("StartPostgres: %v", err)
	}
	dsn, password := first.DSN(), first.password
	sql(t, first, `CREATE TABLE IF NOT EXISTS survive (k text primary key, v text)`)
	sql(t, first, `INSERT INTO survive VALUES ('object','still here')`)
	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	reaped, err := StartPostgres(context.Background(), spec)
	if err != nil {
		t.Fatalf("the re-provision: %v", err)
	}
	if got := sql(t, reaped, `SELECT v FROM survive WHERE k='object'`); got != "still here" {
		t.Fatalf("a reap and a respawn with nothing removed read %q rather than the object: on this "+
			"substrate a cluster is NOT gone because its process was reaped, the data dir under the "+
			"state dir is the cluster, and this negative control would otherwise pass for a reason "+
			"that has nothing to do with the data dir", got)
	}
	if err := reaped.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := os.RemoveAll(spec.DataDir()); err != nil {
		t.Fatalf("removing %s: %v", spec.DataDir(), err)
	}
	if _, err := os.Stat(spec.DataDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s survived the removal, so nothing below is a control: %v", spec.DataDir(), err)
	}

	rebuilt, err := StartPostgres(context.Background(), spec)
	if err != nil {
		t.Fatalf("the re-provision after the removal: %v", err)
	}
	defer func() {
		if err := rebuilt.Stop(context.Background()); err != nil {
			t.Errorf("CLEANUP: %v", err)
		}
	}()
	if !rebuilt.Up() {
		t.Fatal("the store is not up after the removal, so the absence below would be a store that " +
			"failed to start rather than data that is gone")
	}
	if rebuilt.DSN() != dsn || rebuilt.password != password {
		t.Fatalf("the DSN changed across the removal, so an absent object is explained by a "+
			"different store rather than by the removal:\n was: %s\n now: %s", dsn, rebuilt.DSN())
	}
	if got := sql(t, rebuilt, "select count(*) from pg_database where datname='"+PostgresDatabase+"'"); got != "1" {
		t.Fatalf("the rebuilt store did not create its own %s database (count=%q), so it is not a "+
			"store that works and the absent object would prove nothing", PostgresDatabase, got)
	}
	if got := sql(t, rebuilt, `select coalesce(to_regclass('survive')::text,'absent')`); got != "absent" {
		t.Fatalf("the object is still readable after %s was removed: got %q", spec.DataDir(), got)
	}
}
