package f1livetimingreceiver

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseDriverListCanonicalIdentity(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    driverListParseResult
	}{
		{
			name:    "minimal entry",
			payload: `{"44":{"Tla":"AAA"}}`,
			want: driverListParseResult{
				object: true,
				entries: [maxDriverRegistryEntries]driverListEntryPatch{{
					number: 44, object: true, tla: "AAA", tlaState: driverListFieldValid,
				}},
				count: 1,
			},
		},
		{
			name:    "maximum number and four-letter acronym",
			payload: `{"9223372036854775807":{"RacingNumber":"9223372036854775807","Tla":"ABCD"}}`,
			want: driverListParseResult{
				object: true,
				entries: [maxDriverRegistryEntries]driverListEntryPatch{{
					number: 9223372036854775807, object: true, tla: "ABCD",
					tlaState: driverListFieldValid, racingNumberState: driverListFieldValid,
				}},
				count: 1,
			},
		},
		{
			name:    "escaped ASCII identity",
			payload: `{"\u0037":{"RacingNumber":"\u0037","\u0054la":"\u0041BC"}}`,
			want: driverListParseResult{
				object: true,
				entries: [maxDriverRegistryEntries]driverListEntryPatch{{
					number: 7, object: true, tla: "ABC",
					tlaState: driverListFieldValid, racingNumberState: driverListFieldValid,
				}},
				count: 1,
			},
		},
		{
			name:    "non-driver keys ignored",
			payload: `{"0":{"Tla":"AAA"},"01":{"Tla":"AAA"},"+1":{"Tla":"AAA"},"-1":{"Tla":"AAA"},"9223372036854775808":{"Tla":"AAA"},"_kf":true,"Metadata":{}}`,
			want:    driverListParseResult{object: true},
		},
		{
			name:    "unknown values remain opaque",
			payload: `{"7":{"Tla":"ABC","Future":{"\ud800":"\udfff"}}}`,
			want: driverListParseResult{
				object: true,
				entries: [maxDriverRegistryEntries]driverListEntryPatch{{
					number: 7, object: true, tla: "ABC", tlaState: driverListFieldValid,
				}},
				count: 1,
			},
		},
		{
			name:    "malformed unknown entry key is bounded",
			payload: `{"7":{"Tla":"ABC","\ud800":"\udfff"}}`,
			want: driverListParseResult{
				object: true,
				entries: [maxDriverRegistryEntries]driverListEntryPatch{{
					number: 7, object: true, tla: "ABC", tlaState: driverListFieldValid,
				}},
				count:  1,
				issues: driverListIssueUnicode,
			},
		},
		{
			name:    "malformed non-driver key is bounded",
			payload: `{"\ud800":{"Tla":"ABC"},"7":{"Tla":"ABC"}}`,
			want: driverListParseResult{
				object: true,
				entries: [maxDriverRegistryEntries]driverListEntryPatch{{
					number: 7, object: true, tla: "ABC", tlaState: driverListFieldValid,
				}},
				count:  1,
				issues: driverListIssueUnicode,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := []byte(test.payload)
			got, err := parseDriverList(before)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseDriverList() =\n%#v\nwant\n%#v", got, test.want)
			}
			if string(before) != test.payload {
				t.Fatal("parseDriverList mutated its input")
			}
		})
	}
}

func TestParseDriverListEntryFailures(t *testing.T) {
	tests := []struct {
		name              string
		payload           string
		wantTLA           string
		wantTLAState      driverListFieldState
		wantRacingState   driverListFieldState
		wantObject        bool
		wantDuplicate     bool
		wantIssues        driverListIssueSet
		wantCanonicalRows uint8
	}{
		{name: "missing acronym is sparse", payload: `{"7":{}}`, wantObject: true, wantCanonicalRows: 1},
		{name: "one-letter acronym", payload: `{"7":{"Tla":"A"}}`, wantTLA: "A", wantTLAState: driverListFieldValid, wantObject: true, wantCanonicalRows: 1},
		{name: "empty acronym", payload: `{"7":{"Tla":""}}`, wantTLAState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "five-letter acronym", payload: `{"7":{"Tla":"ABCDE"}}`, wantTLAState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "lowercase acronym", payload: `{"7":{"Tla":"AbC"}}`, wantTLAState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "non-ASCII acronym", payload: `{"7":{"Tla":"ÀBC"}}`, wantTLAState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "literal replacement character", payload: `{"7":{"Tla":"�"}}`, wantTLAState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "valid surrogate pair fails only acronym grammar", payload: `{"7":{"Tla":"\uD83D\uDE80"}}`, wantTLAState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "malformed acronym scalar", payload: `{"7":{"Tla":"\ud800"}}`, wantTLAState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueUnicode | driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "matching racing number", payload: `{"7":{"RacingNumber":"7"}}`, wantRacingState: driverListFieldValid, wantObject: true, wantCanonicalRows: 1},
		{name: "mismatched racing number", payload: `{"7":{"RacingNumber":"8"}}`, wantRacingState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "empty racing number", payload: `{"7":{"RacingNumber":""}}`, wantRacingState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "signed racing number", payload: `{"7":{"RacingNumber":"+7"}}`, wantRacingState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "leading-zero racing number", payload: `{"7":{"RacingNumber":"07"}}`, wantRacingState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "overflow racing number", payload: `{"7":{"RacingNumber":"9223372036854775808"}}`, wantRacingState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "numeric racing number", payload: `{"7":{"RacingNumber":7}}`, wantRacingState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "malformed racing-number scalar", payload: `{"7":{"RacingNumber":"\udfff"}}`, wantRacingState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueUnicode | driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "non-object canonical entry", payload: `{"7":null}`, wantTLAState: driverListFieldInvalid, wantIssues: driverListIssueShape | driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "duplicate acronym", payload: `{"7":{"Tla":"ABC","\u0054la":"ABC"}}`, wantTLAState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueShape | driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "duplicate racing number", payload: `{"7":{"RacingNumber":"7","\u0052acingNumber":"7"}}`, wantRacingState: driverListFieldInvalid, wantObject: true, wantIssues: driverListIssueShape | driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "duplicate canonical entry", payload: `{"7":{"Tla":"ABC"},"\u0037":{"Tla":"DEF"}}`, wantTLAState: driverListFieldInvalid, wantRacingState: driverListFieldInvalid, wantObject: true, wantDuplicate: true, wantIssues: driverListIssueShape | driverListIssueIdentity, wantCanonicalRows: 1},
		{name: "duplicate still reports Unicode", payload: `{"7":{"Tla":"ABC"},"\u0037":{"Tla":"\ud800"}}`, wantTLAState: driverListFieldInvalid, wantRacingState: driverListFieldInvalid, wantObject: true, wantDuplicate: true, wantIssues: driverListIssueShape | driverListIssueIdentity | driverListIssueUnicode, wantCanonicalRows: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseDriverList(json.RawMessage(test.payload))
			if err != nil {
				t.Fatal(err)
			}
			if !got.object || got.count != test.wantCanonicalRows || got.issues != test.wantIssues {
				t.Fatalf("parse result object/count/issues = %t/%d/%b, want true/%d/%b", got.object, got.count, got.issues, test.wantCanonicalRows, test.wantIssues)
			}
			entry := got.entries[0]
			wantEntry := driverListEntryPatch{
				number: 7, object: test.wantObject, duplicate: test.wantDuplicate,
				tla: test.wantTLA, tlaState: test.wantTLAState,
				racingNumberState: test.wantRacingState,
			}
			if entry != wantEntry {
				t.Fatalf("entry = %#v, want %#v", entry, wantEntry)
			}
		})
	}
}

func TestParseDriverListRootAndNormalizedInvariants(t *testing.T) {
	for _, payload := range []string{`null`, `[]`, `"value"`, `7`} {
		got, err := parseDriverList(json.RawMessage(payload))
		want := driverListParseResult{issues: driverListIssueShape}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("parseDriverList(%s) = %#v, %v; want %#v, nil", payload, got, err, want)
		}
	}

	for _, payload := range [][]byte{
		[]byte(`{"7":`),
		{'{', '"', 0xff, '"', ':', '1', '}'},
		[]byte(strings.Repeat("[", 10001) + strings.Repeat("]", 10001)),
	} {
		got, err := parseDriverList(payload)
		if !errors.Is(err, errInvalidNormalizedDriverList) || got != (driverListParseResult{}) {
			t.Fatalf("invalid normalized payload result = %#v, %v", got, err)
		}
	}
}

func TestParseDriverListIssueOccurrencesStayBounded(t *testing.T) {
	payload := `{"1":{"Tla":"\ud800","RacingNumber":"2"},"2":null,"3":{"Tla":"ABCDE"},"\udfff":{},"4":{"\ud800":0,"Tla":"AB"}}`
	got, err := parseDriverList(json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	want := driverListIssueUnicode | driverListIssueShape | driverListIssueIdentity
	if got.issues != want {
		t.Fatalf("issues = %b, want %b", got.issues, want)
	}
}
