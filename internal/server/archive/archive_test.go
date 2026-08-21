package archive

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestArchiveThenDropOrdersDeletionAfterEveryHost(t *testing.T) {
	events := []string{}
	failed, err := archiveThenDrop([]int64{11, 22}, func(hostID int64) error {
		events = append(events, fmt.Sprintf("archive:%d", hostID))
		return nil
	}, func() error {
		events = append(events, "drop")
		return nil
	})
	if err != nil || failed != 0 {
		t.Fatalf("archiveThenDrop = failed %d, err %v", failed, err)
	}
	want := []string{"archive:11", "archive:22", "drop"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestArchiveThenDropNeverDeletesAfterAnyHostFailure(t *testing.T) {
	dropped := false
	failed, err := archiveThenDrop([]int64{1, 2, 3}, func(hostID int64) error {
		if hostID == 2 {
			return errors.New("upload failed")
		}
		return nil
	}, func() error {
		dropped = true
		return nil
	})
	if err == nil || failed != 1 {
		t.Fatalf("archiveThenDrop = failed %d, err %v", failed, err)
	}
	if dropped {
		t.Fatal("chunks were dropped after an incomplete archive pass")
	}
}
