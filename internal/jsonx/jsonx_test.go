package jsonx

import "testing"

func obj(t *testing.T, s string) Object {
	t.Helper()
	o, err := ParseObject([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func eq(t *testing.T, got any, want string) {
	t.Helper()
	w, err := Parse([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if !Equal(got, w) {
		t.Fatalf("got  %s\nwant %s", Marshal(got), Marshal(w))
	}
}

func TestMerge(t *testing.T) {
	base := obj(t, `{"model":"a","permissions":{"allow":["x","y"],"defaultMode":"default"},"n":1}`)
	over := obj(t, `{"model":"b","permissions":{"allow":["y","z"]}}`)
	eq(t, Merge(base, over), `{"model":"b","permissions":{"allow":["x","y","z"],"defaultMode":"default"},"n":1}`)
}

func TestDiffAndApply(t *testing.T) {
	from := obj(t, `{"a":1,"b":{"c":true,"d":"x"},"l":["1","2"],"gone":0}`)
	to := obj(t, `{"a":2,"b":{"c":true},"l":["2","3"],"new":{"k":"v"}}`)
	ops := Diff(from, to)
	got := Clone(from).(Object)
	for _, op := range ops {
		switch op.Kind {
		case OpSet:
			Set(got, op.Path, op.Value)
		case OpDelete:
			Delete(got, op.Path)
		case OpAdd:
			AddItem(got, op.Path, op.Value)
		case OpRemove:
			RemoveItem(got, op.Path, op.Value)
		}
	}
	eq(t, got, `{"a":2,"b":{"c":true},"l":["2","3"],"new":{"k":"v"}}`)
	if len(ops) != 6 {
		t.Fatalf("ops = %v", ops)
	}
}

func TestDeletePrunes(t *testing.T) {
	o := obj(t, `{"a":{"b":{"c":1}},"x":1}`)
	if !Delete(o, []string{"a", "b", "c"}) {
		t.Fatal("not deleted")
	}
	eq(t, o, `{"x":1}`)
}

func TestRestrict(t *testing.T) {
	src := obj(t, `{"a":1,"b":{"c":2,"d":3},"l":[1,2,3],"only":true}`)
	other := obj(t, `{"a":9,"b":{"c":9},"l":[2,3,4]}`)
	eq(t, Restrict(src, other), `{"a":1,"b":{"c":2},"l":[2,3]}`)
}

func TestMerge3(t *testing.T) {
	base := obj(t, `{"model":"a","theme":"dark","allow":["x"],"p":{"k":1}}`)
	ours := obj(t, `{"model":"remote","theme":"dark","allow":["x","r"],"p":{"k":1},"remoteOnly":1}`)
	theirs := obj(t, `{"model":"local","allow":["l"],"p":{"k":2}}`)
	eq(t, Merge3(base, ours, theirs), `{"model":"local","allow":["r","l"],"p":{"k":2},"remoteOnly":1}`)
}

func TestNumbersRoundTrip(t *testing.T) {
	o := obj(t, `{"n":1.50,"big":12345678901234567890}`)
	if string(Marshal(o)) != "{\n  \"big\": 12345678901234567890,\n  \"n\": 1.50\n}\n" {
		t.Fatalf("%s", Marshal(o))
	}
}
