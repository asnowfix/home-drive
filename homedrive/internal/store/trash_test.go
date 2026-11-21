package store

import (
	"testing"
	"time"
)

func TestJournal_TrashRecords_Cases(t *testing.T) {
	j := newTestJournal(t)
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	a := TrashRecord{Dir: "/r/.Trash-1000", Name: "f", Path: "/r/f", DeletedAt: at}
	b := TrashRecord{Dir: "/h/Trash", Name: "f", Path: "/r/g", DeletedAt: at}

	for _, r := range []TrashRecord{a, b, a} { // a twice: overwrite, not duplicate
		if err := j.PutTrashRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := j.ListTrashRecords()
	if err != nil || len(got) != 2 {
		t.Fatalf("list = %+v, %v; want 2 records (same name in two dirs)", got, err)
	}
	if err := j.DeleteTrashRecord(a.Dir, a.Name); err != nil {
		t.Fatal(err)
	}
	got, _ = j.ListTrashRecords()
	if len(got) != 1 || got[0].Dir != b.Dir || !got[0].DeletedAt.Equal(at) {
		t.Fatalf("after delete = %+v", got)
	}
}
