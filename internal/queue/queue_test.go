package queue

import (
	"slices"
	"strings"
	"testing"
)

func ids(s string) []string { return strings.Split(s, "") }

type rec struct {
	played  []string
	cleared int
}

func newQueue() (*Queue, *rec) {
	r := &rec{}
	q := New(Events{
		CurrentChanged: func(id string) { r.played = append(r.played, id) },
		CurrentCleared: func() { r.cleared++ },
	})
	return q, r
}

func TestContextAndUpNext(t *testing.T) {
	q, r := newQueue()
	q.PlayContext(Context{Name: "Songs", Ref: "songs", IDs: ids("abcde")}, 1, 0)
	if q.Current() != "b" || strings.Join(q.ContextRest(), "") != "cde" {
		t.Fatalf("current %q rest %v", q.Current(), q.ContextRest())
	}
	q.AddToQueue([]string{"x"})
	q.PlayNext([]string{"y"})
	if strings.Join(q.UpNext(), "") != "yx" {
		t.Errorf("up next %v", q.UpNext())
	}
	for _, want := range []string{"y", "x", "c", "d", "e"} {
		if !q.Next() || q.Current() != want {
			t.Fatalf("next: got %q want %q", q.Current(), want)
		}
	}
	if q.Next() {
		t.Error("next past the end without repeat")
	}
	// Previous walks back through what played, hand-queued tracks included.
	q.Previous()
	q.Previous()
	if q.Current() != "c" {
		t.Errorf("previous: %q", q.Current())
	}
	if len(r.played) == 0 {
		t.Error("no events")
	}
}

func TestReplaceKeepsUpNextAndUndo(t *testing.T) {
	q, _ := newQueue()
	q.PlayContext(Context{Name: "Songs", Ref: "songs", IDs: ids("abc")}, 0, 0)
	q.AddToQueue([]string{"x"})
	if q.PlayContext(Context{Name: "Songs", Ref: "songs", IDs: ids("abc")}, 2, 0) {
		t.Error("the same list reported as replaced")
	}
	if !q.PlayContext(Context{Name: "Album", Ref: "album:k", IDs: ids("pq")}, 0, 12345) {
		t.Error("a different list not reported as replaced")
	}
	if strings.Join(q.UpNext(), "") != "x" || q.Current() != "p" {
		t.Errorf("after replace: current %q up next %v", q.Current(), q.UpNext())
	}
	id, pos, ok := q.Undo()
	if !ok || id != "c" || pos != 12345 || q.Context().Ref != "songs" || strings.Join(q.UpNext(), "") != "x" {
		t.Errorf("undo: %q %d %v, ctx %q, up next %v", id, pos, ok, q.Context().Ref, q.UpNext())
	}
	if _, _, ok := q.Undo(); ok {
		t.Error("undo twice")
	}
}

func TestRepeatAndShuffle(t *testing.T) {
	q, r := newQueue()
	q.PlayContext(Context{Ref: "s", IDs: ids("abc")}, 0, 0)
	q.CycleRepeat() // all
	q.Next()
	q.Next()
	if !q.Next() || q.Current() != "a" {
		t.Errorf("repeat all wrap: %q", q.Current())
	}
	q.CycleRepeat() // one
	n := len(r.played)
	q.OnTrackFinished()
	if q.Current() != "a" || len(r.played) != n+1 {
		t.Error("repeat one")
	}
	q.CycleRepeat() // off

	q.PlayContext(Context{Ref: "big", IDs: ids("abcdefghijklmnop")}, 3, 0)
	q.SetShuffle(true)
	seen := map[string]bool{q.Current(): true}
	for q.Next() {
		if seen[q.Current()] {
			t.Fatal("repeated within a shuffled pass")
		}
		seen[q.Current()] = true
	}
	if len(seen) != 16 {
		t.Errorf("played %d of 16", len(seen))
	}
}

func TestRemove(t *testing.T) {
	q, r := newQueue()
	q.PlayContext(Context{Ref: "s", IDs: ids("abcd")}, 1, 0)
	q.AddToQueue([]string{"c", "z"})
	q.Remove([]string{"c"})
	if strings.Join(q.UpNext(), "") != "z" || strings.Join(q.ContextRest(), "") != "d" {
		t.Errorf("up next %v rest %v", q.UpNext(), q.ContextRest())
	}
	q.Remove([]string{"b"}) // the current one: the next plays
	if q.Current() != "z" {
		t.Errorf("after removing current: %q", q.Current())
	}
	q.Remove([]string{"z", "d", "a"})
	if q.Current() != "" || r.cleared != 1 {
		t.Errorf("nothing left: current %q cleared %d", q.Current(), r.cleared)
	}
}

func TestUpNextEditing(t *testing.T) {
	q, _ := newQueue()
	q.AddToQueue(ids("abcde"))
	q.MoveUpNext([]int{0, 1}, 2) // ab after cd
	if got := strings.Join(q.UpNext(), ""); got != "cdabe" {
		t.Errorf("move: %s", got)
	}
	q.RemoveUpNext([]int{0, 4})
	q.InsertUpNext(1, []string{"x"})
	if got := strings.Join(q.UpNext(), ""); got != "dxab" {
		t.Errorf("remove/insert: %s", got)
	}
	q.PlayUpNextAt(1)
	if q.Current() != "x" || !slices.Equal(q.UpNext(), ids("dab")) {
		t.Errorf("play at: %q %v", q.Current(), q.UpNext())
	}
}
