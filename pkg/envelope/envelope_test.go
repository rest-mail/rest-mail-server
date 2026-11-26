package envelope

import (
	"encoding/json"
	"testing"
)

func TestDataWrapsOneResource(t *testing.T) {
	got, err := json.Marshal(Data[string]{Data: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"data":"x"}` {
		t.Fatalf("Data encodes as %s", got)
	}
}

func TestListWrapsACollectionAndItsPagination(t *testing.T) {
	cases := []struct {
		name string
		list List[int]
		want string
	}{
		{"no pagination", List[int]{Data: []int{1, 2}}, `{"data":[1,2]}`},
		{"last page", List[int]{Data: []int{1}, Pagination: &Pagination{}}, `{"data":[1],"pagination":{"has_more":false}}`},
		{"more to come", List[int]{Data: []int{}, Pagination: &Pagination{Cursor: "c", HasMore: true, Total: 7}},
			`{"data":[],"pagination":{"cursor":"c","has_more":true,"total":7}}`},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.list)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: List encodes as %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestOnlyListCarriesTheListMarker(t *testing.T) {
	type marked interface{ ListEnvelope() }
	if _, ok := any(List[string]{}).(marked); !ok {
		t.Error("List does not carry the list marker")
	}
	if _, ok := any(Data[[]string]{}).(marked); ok {
		t.Error("Data carries the list marker")
	}
}
