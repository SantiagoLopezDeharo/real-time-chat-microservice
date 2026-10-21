package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestNormalizeParticipants(t *testing.T) {
	tests := []struct {
		in   []string
		want []string
	}{
		{[]string{"alice"}, []string{"alice"}},
		{[]string{"bob", "alice"}, []string{"alice", "bob"}},
		{[]string{"bob", "alice", "bob", "alice"}, []string{"alice", "bob"}},
		{[]string{" bob ", "alice\t", "\nalice"}, []string{"alice", "bob"}},
		{[]string{"Bob", "bob"}, []string{"Bob", "bob"}},
		{[]string{"z", "a", "m", "a", "z"}, []string{"a", "m", "z"}},
		{[]string{"日本", "émile", "alice"}, []string{"alice", "émile", "日本"}},
		{[]string{"a,b", "c"}, []string{"a,b", "c"}},
	}
	for _, tt := range tests {
		input := slices.Clone(tt.in)
		got, err := NormalizeParticipants(tt.in)
		if err != nil {
			t.Errorf("NormalizeParticipants(%q): %v", tt.in, err)
			continue
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("NormalizeParticipants(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if !slices.Equal(tt.in, input) {
			t.Errorf("NormalizeParticipants mutated its input: %q", tt.in)
		}
	}
}

func TestNormalizeParticipantsIsOrderIndependent(t *testing.T) {
	a, _ := NormalizeParticipants([]string{"carol", "alice", "bob"})
	b, _ := NormalizeParticipants([]string{"bob", "carol", "alice", "bob"})
	if !slices.Equal(a, b) {
		t.Errorf("%q != %q", a, b)
	}
}

func TestNormalizeParticipantsErrors(t *testing.T) {
	tooMany := make([]string, MaxParticipants+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprint("user-", i)
	}
	tests := []struct {
		in   []string
		want error
	}{
		{nil, ErrNoParticipants},
		{[]string{}, ErrNoParticipants},
		{[]string{""}, ErrEmptyParticipant},
		{[]string{"alice", " "}, ErrEmptyParticipant},
		{[]string{"alice", "\t\n"}, ErrEmptyParticipant},
		{tooMany, ErrTooManyParticipants},
	}
	for _, tt := range tests {
		if _, err := NormalizeParticipants(tt.in); !errors.Is(err, tt.want) {
			t.Errorf("NormalizeParticipants(%.40q) error = %v, want %v", tt.in, err, tt.want)
		}
	}
	if _, err := NormalizeParticipants(tooMany[:MaxParticipants]); err != nil {
		t.Errorf("exactly MaxParticipants rejected: %v", err)
	}
}

func FuzzNormalizeParticipants(f *testing.F) {
	f.Add("alice,bob,alice")
	f.Add(" , ")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		got, err := NormalizeParticipants(strings.Split(raw, ","))
		if err != nil {
			return
		}
		if !slices.IsSorted(got) {
			t.Fatalf("not sorted: %q", got)
		}
		for i := 1; i < len(got); i++ {
			if got[i] == got[i-1] {
				t.Fatalf("duplicate %q", got[i])
			}
		}
		for _, p := range got {
			if p == "" || strings.TrimSpace(p) != p {
				t.Fatalf("untrimmed participant %q", p)
			}
		}
		again, err := NormalizeParticipants(got)
		if err != nil || !slices.Equal(again, got) {
			t.Fatalf("not idempotent: %q -> %q (%v)", got, again, err)
		}
	})
}

func TestContainsUser(t *testing.T) {
	participants := []string{"alice", "bob"}
	if !ContainsUser(participants, "alice") || !ContainsUser(participants, "bob") {
		t.Error("member not found")
	}
	for _, outsider := range []string{"", "carol", "Alice", "alice "} {
		if ContainsUser(participants, outsider) {
			t.Errorf("%q reported as member", outsider)
		}
	}
}

func TestMessageWireFormat(t *testing.T) {
	id := bson.NewObjectID()
	created := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC)
	msg := Message{ID: id, Sender: "alice", Content: "hi", CreatedAt: created, Participants: []string{"alice", "bob"}}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"` + id.Hex() + `","sender":"alice","content":"hi","created_at":"2026-01-02T03:04:05.006Z","participants":["alice","bob"]}`
	if string(data) != want {
		t.Errorf("json = %s\nwant   %s", data, want)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != id || !decoded.CreatedAt.Equal(created) {
		t.Errorf("round trip = %+v", decoded)
	}

	raw, err := bson.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["_id"] != id {
		t.Errorf("bson _id = %v, want ObjectID %v", doc["_id"], id)
	}
	for _, key := range []string{"sender", "content", "created_at", "participants"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("bson document lacks %q", key)
		}
	}
}

func BenchmarkNormalizeParticipants(b *testing.B) {
	group := []string{"zoe", "alice", "bob", "carol", "dave", "alice", "erin"}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = NormalizeParticipants(group)
	}
}
